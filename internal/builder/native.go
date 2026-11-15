package builder

// Native packages (docs/NATIVE.md): a llama.cpp chat model, its llama.cpp
// runtime for one operating system, the llmpack launcher and the model's
// agent harness, zipped into packages/<name>.zip. Unzipped on that system it
// runs without Docker or Podman, on the GPU (Metal on a Mac).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/native"
	"github.com/seanbrill/local-llm/internal/version"
)

// DefaultNativeCtx is a package's context window when none is chosen.
const DefaultNativeCtx = 8192

// NativeOptions configures one native package.
type NativeOptions struct {
	Model  catalog.Model
	Target string // native.Target ID, e.g. "macos-arm64"
	// Name is the package's file name without ".zip"; "" = <model id>-<target>.
	Name         string
	SystemPrompt string
	InjectMode   string // "missing" (default) | "always"
	CtxSize      int    // tokens; 0 = DefaultNativeCtx
	// Harness is the agent harness to include (nil = none). The shell tool is
	// dropped: nothing sandboxes it outside a container.
	Harness *agent.Config
	// FromImage is the image whose settings these are ("" = the catalog entry).
	FromImage string
}

// NativePackage describes a finished package: packages/<name>.json, next to
// packages/<name>.zip and packages/<name>.zip.sha256.
type NativePackage struct {
	Name         string    `json:"name"`
	File         string    `json:"file"` // <name>.zip
	SizeBytes    int64     `json:"size_bytes"`
	SHA256       string    `json:"sha256,omitempty"`
	ModelID      string    `json:"model_id"`
	ModelName    string    `json:"model_name"`
	Target       string    `json:"target"`
	TargetLabel  string    `json:"target_label"`
	Created      time.Time `json:"created"`
	CtxSize      int       `json:"ctx_size,omitempty"`
	SystemPrompt bool      `json:"system_prompt"`
	InjectMode   string    `json:"inject_mode,omitempty"`
	Toolsets     []string  `json:"toolsets"` // the harness's; empty = no harness
	LlamaCpp     string    `json:"llama_cpp"`
	License      string    `json:"license,omitempty"`
	// Shareable is true when the model's license lets anybody redistribute it
	// (filefreak.Redistributable); false also when it couldn't be read.
	Shareable bool   `json:"shareable"`
	SourceURL string `json:"source_url,omitempty"`
	FromImage string `json:"from_image,omitempty"`
	Factory   string `json:"factory_version,omitempty"`
}

var packageNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$`)

// DefaultPackageName is <model id>-<target>, made safe for a file name.
func DefaultPackageName(modelID, target string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(modelID) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-.")
	if s == "" {
		s = "model"
	}
	return s + "-" + target
}

// ValidPackageName reports whether name can be a package's file name.
func ValidPackageName(name string) bool {
	return packageNameRe.MatchString(name) && !strings.HasSuffix(name, ".") && !strings.Contains(name, "..")
}

// PackagesDir is where native packages are written.
func (b *Builder) PackagesDir() string { return filepath.Join(b.BaseDir, "packages") }

// NativeCapable reports why a model can't be packaged natively ("" = it can).
func NativeCapable(m catalog.Model) string {
	if modelFamily(m) != "llama" {
		return fmt.Sprintf("native packages run llama.cpp chat models for now; %s is a %s model, which stays container-only", m.Name, kindName(m))
	}
	if m.Mod() == "embedding" {
		return fmt.Sprintf("native packages run chat models for now; %s is an embedding model", m.Name)
	}
	return ""
}

func kindName(m catalog.Model) string {
	if m.Rt() == "python" {
		return "Python-runtime"
	}
	return m.Mod()
}

// NormalizeNativeHarness is the harness config a package carries: the shell
// toolset removed (dropped says it was asked for), no workspace or skills
// path (the launcher sets them on the computer the package runs on), no API
// key, defaults filled in. nil stays nil.
func NormalizeNativeHarness(h *agent.Config) (cfg *agent.Config, dropped bool, err error) {
	if h == nil {
		return nil, false, nil
	}
	c := *h
	c.Search.APIKey = ""
	c.Shell = agent.ShellConfig{}
	if c.Tools != nil {
		kept := make([]string, 0, len(c.Tools))
		for _, t := range c.Tools {
			if t == agent.ToolsetShell || t == agent.ToolRunCommand {
				dropped = true
				continue
			}
			kept = append(kept, t)
		}
		c.Tools = kept
	}
	c = c.WithDefaults()
	// Validate with stand-in folders (the real ones exist only at run time).
	v := c
	v.Workspace, v.SkillsDir = "/workspace", "/skills"
	if err := v.Validate(); err != nil {
		return nil, false, err
	}
	c.Workspace, c.SkillsDir = "", ""
	return &c, dropped, nil
}

// pkgFile is one regular file of a package, read from disk or held in memory.
type pkgFile struct {
	rel  string // path inside the package folder
	data []byte // small files
	path string // or a file on disk
	mode fs.FileMode
	big  bool // stored, not deflated (the weights)
}

type pkgLink struct{ rel, target string }

// BuildNativePackage makes packages/<name>.zip. Progress goes to log, like
// an image build's.
func (b *Builder) BuildNativePackage(ctx context.Context, o NativeOptions, log LogFunc) (*NativePackage, error) {
	t, ok := native.TargetByID(o.Target)
	if !ok {
		var ids []string
		for _, x := range native.Targets() {
			ids = append(ids, x.ID)
		}
		return nil, fmt.Errorf("unknown target %q (one of %s)", o.Target, strings.Join(ids, ", "))
	}
	m := o.Model
	if why := NativeCapable(m); why != "" {
		return nil, errors.New(why)
	}
	name := o.Name
	if name == "" {
		name = DefaultPackageName(m.ID, t.ID)
	}
	if !ValidPackageName(name) {
		return nil, fmt.Errorf("%q can't be a package name: use letters, digits, dots, dashes and underscores", name)
	}
	hcfg, droppedShell, err := NormalizeNativeHarness(o.Harness)
	if err != nil {
		return nil, err
	}
	ctxSize := o.CtxSize
	if ctxSize <= 0 {
		ctxSize = DefaultNativeCtx
	}
	inject := injectMode(o.InjectMode)

	log(fmt.Sprintf("Making a native package of %s for %s.", m.Name, t.Label))
	dir := b.PackagesDir()
	// Only one package is made at a time (the server queues them), so what
	// is in .work is left over from a factory that stopped mid-package.
	_ = os.RemoveAll(filepath.Join(dir, ".work"))
	work := filepath.Join(dir, ".work", fmt.Sprintf("pkg-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	// 1. The weights, downloaded like a build's when they aren't here yet.
	modelPath, err := b.EnsureModel(ctx, m, log)
	if err != nil {
		return nil, err
	}
	mmPath, err := b.EnsureMMProj(ctx, m, log)
	if err != nil {
		return nil, err
	}
	weights := fileSize(modelPath) + fileSize(mmPath)
	if _, avail, err := DiskSpace(dir); err == nil && avail < uint64(weights)+(512<<20) {
		return nil, fmt.Errorf("the package needs about %.1f GB free next to packages/, and the disk has %.1f GB: free some space (Library, Clean up) and try again",
			float64(weights+(512<<20))/1e9, float64(avail)/1e9)
	}

	// 2. llama.cpp for the target, from the release cache.
	rt, err := b.ensureLlamaRuntime(ctx, t, log)
	if err != nil {
		return nil, err
	}
	// 3. The launcher, built for the target.
	launcherPath, err := b.nativeLauncher(ctx, t, work, log)
	if err != nil {
		return nil, err
	}
	// 4. The model's license and notices, from its repository.
	notes := modelNotices(ctx, m, log)

	// 5. What goes in.
	now := time.Now()
	var files []pkgFile
	var links []pkgLink
	manifest := &native.Manifest{
		Schema: native.ManifestSchema, Name: m.Name, ModelID: m.ID, Target: t.ID,
		InjectMode: inject, License: notes.License, SourceURL: notes.SourceURL,
		Created: now.UTC().Format(time.RFC3339), Factory: version.Get().Version,
	}
	eng := native.Engine{
		Kind: "llama.cpp", Role: native.RoleChat, Release: native.LlamaCppRelease, Build: native.LlamaCppBuild,
		Server: "bin/llama/" + t.ServerName(), Accel: t.Accel, GPULayers: t.GPULayers,
		Model: "model/" + filepath.Base(m.File), CtxSize: ctxSize, Modality: m.Mod(),
	}
	files = append(files, pkgFile{rel: eng.Model, path: modelPath, mode: 0o644, big: true})
	if mmPath != "" {
		eng.MMProj = "model/" + filepath.Base(m.MMProjFile)
		if eng.MMProj == eng.Model {
			eng.MMProj = "model/mmproj-" + filepath.Base(m.MMProjFile)
		}
		files = append(files, pkgFile{rel: eng.MMProj, path: mmPath, mode: 0o644, big: true})
	}
	manifest.Engines = []native.Engine{eng}
	for _, f := range rt.Files {
		if f.Link != "" {
			links = append(links, pkgLink{rel: "bin/llama/" + f.Name, target: f.Link})
			continue
		}
		files = append(files, pkgFile{rel: "bin/llama/" + f.Name, path: f.Path, mode: f.Mode})
	}
	ownLicense := false
	for _, f := range rt.Licenses {
		files = append(files, pkgFile{rel: "licenses/llama.cpp/" + f.Name, path: f.Path, mode: 0o644})
		ownLicense = ownLicense || strings.EqualFold(f.Name, "LICENSE")
	}
	if !ownLicense { // the Windows archive carries only its OpenMP license
		files = append(files, pkgFile{rel: "licenses/llama.cpp/LICENSE", data: []byte(llamaCppLicense), mode: 0o644})
	}
	files = append(files, pkgFile{rel: "bin/" + t.LauncherName(), path: launcherPath, mode: 0o755})
	for _, n := range notes.Files {
		files = append(files, pkgFile{rel: n.Rel, data: n.Data, mode: 0o644})
	}
	if strings.TrimSpace(o.SystemPrompt) != "" {
		manifest.SystemPromptFile = "config/system_prompt.txt"
		files = append(files, pkgFile{rel: manifest.SystemPromptFile, data: []byte(o.SystemPrompt), mode: 0o644})
		log(fmt.Sprintf("Including the initialization prompt (%d chars, mode=%s).", len(o.SystemPrompt), inject))
	}
	var toolsets []string
	if hcfg != nil {
		manifest.HarnessFile = "harness/harness.json"
		data, _ := json.MarshalIndent(hcfg, "", "  ")
		files = append(files, pkgFile{rel: manifest.HarnessFile, data: append(data, '\n'), mode: 0o644})
		toolsets = hcfg.EnabledToolsets()
		on := hcfg.EnabledTools()
		skills := 0
		if on[agent.ToolListSkills] || on[agent.ToolViewSkill] {
			manifest.SkillsDir = "harness/skills"
			staged := filepath.Join(work, "skills")
			if err := os.MkdirAll(staged, 0o755); err != nil {
				return nil, err
			}
			if skills, err = stageSkills(filepath.Join(b.BaseDir, "skills"), staged); err != nil {
				return nil, err
			}
			sf, err := walkFiles(staged, manifest.SkillsDir)
			if err != nil {
				return nil, err
			}
			files = append(files, sf...)
		}
		msg := "Including the agent harness: " + strings.Join(toolsets, ", ")
		if skills > 0 {
			msg += fmt.Sprintf(" (%d skills)", skills)
		}
		if on[agent.ToolListFiles] || on[agent.ToolWriteFile] {
			msg += "; its file tools work in the package's workspace folder"
		}
		log(msg + ".")
	}
	if droppedShell {
		log("The shell tool is left out: a native package has no container to keep it away from the rest of the computer.")
	}
	mdata, err := manifest.Marshal()
	if err != nil {
		return nil, err
	}
	files = append(files, pkgFile{rel: native.ManifestName, data: mdata, mode: 0o644})
	readme := packageReadme(readmeInfo{m: m, t: t, man: manifest, eng: eng, notes: notes, toolsets: toolsets,
		hcfg: hcfg, prompt: o.SystemPrompt != "", weightsBytes: weights, fromImage: o.FromImage, created: now})
	files = append(files, pkgFile{rel: native.ReadmeName, data: []byte(readme), mode: 0o644})
	start := startScript(t, m.Name)
	files = append(files, pkgFile{rel: start.name, data: []byte(start.body), mode: 0o755})

	// 6. The zip, written next to its final name and renamed when complete.
	final := filepath.Join(dir, name+".zip")
	part := filepath.Join(dir, "."+name+".zip.part")
	sum, size, err := writePackageZip(ctx, part, name, files, links, hcfg != nil, now, log)
	if err != nil {
		os.Remove(part)
		return nil, err
	}
	if err := os.Rename(part, final); err != nil {
		os.Remove(part)
		return nil, err
	}
	_ = os.WriteFile(final+".sha256", []byte(sum+"  "+name+".zip\n"), 0o644)
	pkg := &NativePackage{
		Name: name, File: name + ".zip", SizeBytes: size, SHA256: sum,
		ModelID: m.ID, ModelName: m.Name, Target: t.ID, TargetLabel: t.Label, Created: now.UTC(),
		CtxSize: ctxSize, SystemPrompt: manifest.SystemPromptFile != "", InjectMode: inject,
		Toolsets: append([]string{}, toolsets...), LlamaCpp: native.LlamaCppRelease + " (" + native.LlamaCppBuild + ")",
		License: notes.License, Shareable: notes.Shareable, SourceURL: notes.SourceURL, FromImage: o.FromImage,
		Factory: version.Get().Version,
	}
	if meta, err := json.MarshalIndent(pkg, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, name+".json"), append(meta, '\n'), 0o644)
	}
	log(fmt.Sprintf("Package ready: packages/%s.zip (%.2f GB)", name, float64(size)/1e9))
	log("SHA-256 of the zip: " + sum)
	return pkg, nil
}

func fileSize(p string) int64 {
	if p == "" {
		return 0
	}
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// walkFiles lists the regular files under dir as package files under prefix.
func walkFiles(dir, prefix string) ([]pkgFile, error) {
	var out []pkgFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, pkgFile{rel: path.Join(prefix, filepath.ToSlash(rel)), path: p, mode: 0o644})
		return nil
	})
	return out, err
}

// ctxReader stops a copy when the package is canceled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// pctReader logs "  <n>% (<a> / <b> GB)" every 10% of a large file.
type pctReader struct {
	r     io.Reader
	total int64
	read  int64
	next  int
	log   LogFunc
}

func (p *pctReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.total > 0 {
		if pct := int(p.read * 100 / p.total); pct >= p.next+10 || (pct == 100 && p.next < 100) {
			p.next = pct - pct%10
			p.log(fmt.Sprintf("  %d%% (%.2f / %.2f GB)", pct, float64(p.read)/1e9, float64(p.total)/1e9))
		}
	}
	return n, err
}

// writePackageZip writes the package into one top folder, SHA256SUMS last,
// and returns the zip's SHA-256 and size.
func writePackageZip(ctx context.Context, dest, top string, files []pkgFile, links []pkgLink, workspace bool, now time.Time, log LogFunc) (string, int64, error) {
	out, err := os.Create(dest)
	if err != nil {
		return "", 0, err
	}
	defer out.Close()
	zsum := sha256.New()
	counted := &countWriter{w: io.MultiWriter(out, zsum)}
	z := newZipWriter(counted)

	// Small files first, the weights last, so the README and the start
	// script are at the front of the archive.
	sort.SliceStable(files, func(i, j int) bool { return !files[i].big && files[j].big })
	dirs := map[string]bool{}
	addDirs := func(rel string) {
		for d := path.Dir(rel); d != "." && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	for _, f := range files {
		addDirs(f.rel)
	}
	for _, l := range links {
		addDirs(l.rel)
	}
	if workspace {
		dirs[native.WorkspaceDir] = true
	}
	var dirList []string
	for d := range dirs {
		dirList = append(dirList, d)
	}
	sort.Strings(dirList)
	if err := z.AddDir(top, now); err != nil {
		return "", 0, err
	}
	for _, d := range dirList {
		if err := z.AddDir(top+"/"+d, now); err != nil {
			return "", 0, err
		}
	}

	var sums []native.Checksum
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		name := top + "/" + f.rel
		if !f.big {
			data := f.data
			if data == nil {
				if data, err = os.ReadFile(f.path); err != nil {
					return "", 0, err
				}
			}
			if err := z.AddBytes(name, data, f.mode, now); err != nil {
				return "", 0, err
			}
			h := sha256.Sum256(data)
			sums = append(sums, native.Checksum{SHA256: hex.EncodeToString(h[:]), Path: f.rel})
			continue
		}
		sum, err := addBigFile(ctx, z, name, f, now, log)
		if err != nil {
			return "", 0, err
		}
		sums = append(sums, native.Checksum{SHA256: sum, Path: f.rel})
	}
	for _, l := range links {
		if err := z.AddSymlink(top+"/"+l.rel, l.target, now); err != nil {
			return "", 0, err
		}
	}
	if err := z.AddBytes(top+"/"+native.ChecksumsName, native.FormatChecksums(sums), 0o644, now); err != nil {
		return "", 0, err
	}
	if err := z.Close(); err != nil {
		return "", 0, err
	}
	if err := out.Sync(); err != nil {
		return "", 0, err
	}
	if err := out.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(zsum.Sum(nil)), counted.n, nil
}

// addBigFile stores a weights file: one pass for its CRC-32 and SHA-256 (the
// local header needs the CRC first), one to copy it.
func addBigFile(ctx context.Context, z *zipWriter, name string, f pkgFile, now time.Time, log LogFunc) (string, error) {
	in, err := os.Open(f.path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return "", err
	}
	size := fi.Size()
	base := path.Base(f.rel)
	log(fmt.Sprintf("Checking %s (%.2f GB) ...", base, float64(size)/1e9))
	crc := crc32.NewIEEE()
	sha := sha256.New()
	if _, err := io.Copy(io.MultiWriter(crc, sha), &pctReader{r: ctxReader{ctx, in}, total: size, log: log}); err != nil {
		return "", err
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	log(fmt.Sprintf("Packing %s ...", base))
	if err := z.AddStored(name, &pctReader{r: ctxReader{ctx, in}, total: size, log: log}, size, crc.Sum32(), f.mode, now, nil); err != nil {
		return "", err
	}
	return hex.EncodeToString(sha.Sum(nil)), nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// ---- the packages folder ----------------------------------------------------

// NativePackages lists packages/*.zip, newest first, with what their
// metadata files say.
func (b *Builder) NativePackages() []NativePackage {
	dir := b.PackagesDir()
	zips, _ := filepath.Glob(filepath.Join(dir, "*.zip"))
	out := make([]NativePackage, 0, len(zips))
	for _, z := range zips {
		name := strings.TrimSuffix(filepath.Base(z), ".zip")
		if !ValidPackageName(name) {
			continue
		}
		fi, err := os.Stat(z)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		p := NativePackage{Name: name, Created: fi.ModTime().UTC(), Toolsets: []string{}}
		if data, err := os.ReadFile(filepath.Join(dir, name+".json")); err == nil {
			_ = json.Unmarshal(data, &p)
		}
		p.Name, p.File, p.SizeBytes = name, name+".zip", fi.Size()
		if p.Toolsets == nil {
			p.Toolsets = []string{}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// NativePackagePath is the zip of a package, if it exists.
func (b *Builder) NativePackagePath(name string) (string, error) {
	if !ValidPackageName(name) {
		return "", fmt.Errorf("%q isn't a package name", name)
	}
	p := filepath.Join(b.PackagesDir(), name+".zip")
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("no package %s", name)
	}
	return p, nil
}

// RemoveNativePackage deletes a package's zip and the files beside it.
func (b *Builder) RemoveNativePackage(name string) error {
	p, err := b.NativePackagePath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		return err
	}
	_ = os.Remove(p + ".sha256")
	_ = os.Remove(filepath.Join(b.PackagesDir(), name+".json"))
	return nil
}
