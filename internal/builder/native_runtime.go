package builder

// llama.cpp's prebuilt release for a native package's target: downloaded
// once into packages/.cache, checked against the SHA-256 pinned in
// internal/native, and unpacked to just what llama-server needs (the server,
// the shared libraries, the license files). The other llama.cpp tools stay out.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/seanbrill/local-llm/internal/native"
)

const (
	maxRuntimeArchive = 1 << 30   // the largest llama.cpp download accepted
	maxRuntimeFile    = 512 << 20 // the largest single file unpacked
	runtimeIndex      = "runtime.json"
)

// runtimeFile is one file of an unpacked llama.cpp release.
type runtimeFile struct {
	Name string      `json:"name"`           // file name (the archives are flat)
	Mode fs.FileMode `json:"mode"`           // permissions
	Link string      `json:"link,omitempty"` // a symbolic link's target (a sibling's name)
	Path string      `json:"-"`              // on disk, in the cache
}

// llamaRuntime is the unpacked release for one target.
type llamaRuntime struct {
	Dir      string        `json:"-"`
	SHA256   string        `json:"archive_sha256"` // the archive it came from
	Files    []runtimeFile `json:"files"`          // llama-server and its libraries
	Licenses []runtimeFile `json:"licenses"`
}

func (b *Builder) llamaCacheDir() string {
	return filepath.Join(b.PackagesDir(), ".cache", "llama.cpp", native.LlamaCppBuild)
}

// ensureLlamaRuntime returns the target's unpacked release, downloading and
// unpacking it the first time.
func (b *Builder) ensureLlamaRuntime(ctx context.Context, t native.Target, log LogFunc) (*llamaRuntime, error) {
	cache := b.llamaCacheDir()
	dir := filepath.Join(cache, t.ID)
	if rt, err := readRuntime(dir); err == nil && rt.SHA256 == t.AssetSHA256 {
		log(fmt.Sprintf("llama.cpp %s (%s) for %s: from the cache.", native.LlamaCppRelease, native.LlamaCppBuild, t.Label))
		return rt, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	archive := filepath.Join(cache, t.Asset)
	if err := downloadVerified(ctx, t.AssetURL(), archive, t.AssetSHA256, t.Asset, log); err != nil {
		return nil, err
	}
	defer os.Remove(archive) // the unpacked files are what's kept
	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	rt, err := unpackRuntime(archive, tmp, t)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("unpack %s: %w", t.Asset, err)
	}
	if err := checkBinary(filepath.Join(tmp, t.ServerName()), t); err != nil {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("llama.cpp's %s: %w", t.ServerName(), err)
	}
	rt.SHA256 = t.AssetSHA256
	idx, _ := json.MarshalIndent(rt, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, runtimeIndex), idx, 0o644); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	log(fmt.Sprintf("Unpacked llama.cpp %s (%s) for %s: llama-server and %d libraries.", native.LlamaCppRelease, native.LlamaCppBuild, t.Label, len(rt.Files)-1))
	return readRuntime(dir)
}

// readRuntime reads an unpacked release's index and checks its files are there.
func readRuntime(dir string) (*llamaRuntime, error) {
	data, err := os.ReadFile(filepath.Join(dir, runtimeIndex))
	if err != nil {
		return nil, err
	}
	var rt llamaRuntime
	if err := json.Unmarshal(data, &rt); err != nil {
		return nil, err
	}
	rt.Dir = dir
	for _, list := range [][]runtimeFile{rt.Files, rt.Licenses} {
		for i := range list {
			if list[i].Link != "" {
				continue
			}
			list[i].Path = filepath.Join(dir, list[i].Name)
			if _, err := os.Stat(list[i].Path); err != nil {
				return nil, err
			}
		}
	}
	return &rt, nil
}

// runtimeKind sorts an archive member: the server, a library, a license, or
// something a package doesn't need ("").
func runtimeKind(name string, t native.Target) string {
	lower := strings.ToLower(name)
	switch {
	case name == t.ServerName():
		return "server"
	case strings.HasSuffix(lower, ".dylib"), strings.HasSuffix(lower, ".dll"),
		strings.HasSuffix(lower, ".so"), strings.Contains(lower, ".so."):
		return "lib"
	case strings.HasPrefix(lower, "license"):
		return "license"
	}
	return ""
}

// memberName is an archive member's file name, when it sits at the top of
// the archive or in its one top folder ("llama-b12110/"); ok is false for
// anything deeper or unsafe.
func memberName(name string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) || strings.Contains(name, "..") {
		return "", false
	}
	parts := strings.Split(strings.TrimSuffix(name, "/"), "/")
	switch len(parts) {
	case 1:
		return parts[0], true
	case 2:
		return parts[1], true
	}
	return "", false
}

func unpackRuntime(archive, dir string, t native.Target) (*llamaRuntime, error) {
	rt := &llamaRuntime{}
	add := func(name string, mode fs.FileMode, link string, r io.Reader) error {
		kind := runtimeKind(name, t)
		if kind == "" {
			return nil
		}
		f := runtimeFile{Name: name, Mode: mode.Perm(), Link: link}
		if link == "" {
			out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			n, err := io.Copy(out, io.LimitReader(r, maxRuntimeFile+1))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			if n > maxRuntimeFile {
				return fmt.Errorf("%s is larger than %d MB", name, maxRuntimeFile>>20)
			}
		}
		if kind == "server" || kind == "lib" {
			if f.Mode&0o111 == 0 {
				f.Mode = 0o755 // programs and libraries stay executable
			}
			rt.Files = append(rt.Files, f)
		} else {
			f.Mode = 0o644
			rt.Licenses = append(rt.Licenses, f)
		}
		return nil
	}
	var err error
	if strings.HasSuffix(t.Asset, ".zip") {
		err = unpackZip(archive, add)
	} else {
		err = unpackTarGz(archive, add)
	}
	if err != nil {
		return nil, err
	}
	// A link must point at a file that was unpacked (links to links included).
	have := map[string]bool{}
	for _, f := range rt.Files {
		have[f.Name] = true
	}
	server := false
	for _, f := range rt.Files {
		if f.Link != "" && !have[f.Link] {
			return nil, fmt.Errorf("%s links to %s, which isn't in the archive", f.Name, f.Link)
		}
		server = server || (f.Name == t.ServerName() && f.Link == "")
	}
	if !server {
		return nil, fmt.Errorf("no %s in the archive", t.ServerName())
	}
	return rt, nil
}

type addFunc func(name string, mode fs.FileMode, link string, r io.Reader) error

func unpackTarGz(archive string, add addFunc) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name, ok := memberName(h.Name)
		if !ok {
			continue
		}
		switch h.Typeflag {
		case tar.TypeReg:
			if err := add(name, fs.FileMode(h.Mode), "", tr); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if strings.ContainsAny(h.Linkname, `/\`) || h.Linkname == ".." || h.Linkname == "." || h.Linkname == "" {
				continue // only links to a sibling are kept
			}
			if err := add(name, 0o755, h.Linkname, nil); err != nil {
				return err
			}
		}
	}
}

func unpackZip(archive string, add addFunc) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		name, ok := memberName(f.Name)
		if !ok {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		err = add(name, 0o755, "", r)
		r.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// downloadVerified downloads url to dest unless dest is already there with
// the wanted SHA-256, and checks the download against it.
func downloadVerified(ctx context.Context, url, dest, want, label string, log LogFunc) error {
	if sum, err := native.FileSHA256(dest); err == nil && sum == want {
		return nil
	}
	host := url
	if u, err := neturl.Parse(url); err == nil && u.Host != "" {
		host = u.Host
	}
	log(fmt.Sprintf("Downloading %s from %s ...", label, host))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download of %s failed: HTTP %d", label, resp.StatusCode)
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	pr := &progressReader{r: io.LimitReader(resp.Body, maxRuntimeArchive+1), total: resp.ContentLength, log: log, label: label}
	n, err := io.Copy(io.MultiWriter(f, h), pr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxRuntimeArchive {
		err = fmt.Errorf("%s is larger than %d MB", label, maxRuntimeArchive>>20)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("download of %s interrupted: %w", label, err)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != want {
		os.Remove(tmp)
		return fmt.Errorf("the download of %s doesn't match the SHA-256 the factory pins for it (got %s, want %s), so it was deleted", label, sum, want)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	log(fmt.Sprintf("Saved %s (SHA-256 checked)", label))
	return nil
}

// lcCodeSignature is the Mach-O load command of an embedded code signature.
const lcCodeSignature = 0x1d

// checkBinary checks a program is for the target: its format and CPU, and on
// Apple Silicon that it carries a code signature (the kernel refuses to run
// arm64 code that has none; an ad-hoc signature is enough).
func checkBinary(p string, t native.Target) error {
	switch t.GOOS {
	case "darwin":
		f, err := macho.Open(p)
		if err != nil {
			return fmt.Errorf("not a macOS program: %w", err)
		}
		defer f.Close()
		want := macho.CpuAmd64
		if t.GOARCH == "arm64" {
			want = macho.CpuArm64
		}
		if f.Cpu != want {
			return fmt.Errorf("built for %v, not %v", f.Cpu, want)
		}
		if t.GOARCH == "arm64" && !machoSigned(f) {
			return errors.New("it has no code signature, and Apple Silicon Macs only run signed programs")
		}
	case "linux":
		f, err := elf.Open(p)
		if err != nil {
			return fmt.Errorf("not a Linux program: %w", err)
		}
		defer f.Close()
		want := elf.EM_X86_64
		if t.GOARCH == "arm64" {
			want = elf.EM_AARCH64
		}
		if f.Machine != want {
			return fmt.Errorf("built for %v, not %v", f.Machine, want)
		}
	case "windows":
		f, err := pe.Open(p)
		if err != nil {
			return fmt.Errorf("not a Windows program: %w", err)
		}
		defer f.Close()
		want := uint16(pe.IMAGE_FILE_MACHINE_AMD64)
		if t.GOARCH == "arm64" {
			want = pe.IMAGE_FILE_MACHINE_ARM64
		}
		if f.Machine != want {
			return fmt.Errorf("built for machine %#x, not %#x", f.Machine, want)
		}
	}
	return nil
}

func machoSigned(f *macho.File) bool {
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) >= 4 && f.ByteOrder.Uint32(raw[:4]) == lcCodeSignature {
			return true
		}
	}
	return false
}
