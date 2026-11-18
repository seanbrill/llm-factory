package filefreak

// Hugging Face: what a catalog URL names, the official SHA-256 and revision of
// the file it points at, and the repository's license and gate.
//
// Every catalog URL has the form
//
//	https://huggingface.co/<org>/<repo>/resolve/<ref>/<path in repo>[?download=true]
//
// A HEAD request on it answers (for a file kept in LFS, which every weight is)
// with a redirect to the CDN that carries X-Repo-Commit (the commit <ref> is
// at), X-Linked-Etag (the file's SHA-256, quoted) and X-Linked-Size. A
// renamed repository answers with a relative redirect to its new name first,
// which is followed; the CDN redirect is not.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultHFBase is where Hugging Face is.
const DefaultHFBase = "https://huggingface.co"

var (
	hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hexCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// A repository's owner and name, as Hugging Face allows them.
	repoPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
)

// HFFile is one file of a Hugging Face model repository, as a URL names it.
type HFFile struct {
	Repo string // "org/name"
	Ref  string // the revision the URL asks for: "main", or a commit
	Path string // the file's path in the repository
}

// ArchivePath is where a file of repo at commit lives in the archive.
func ArchivePath(repo, commit, file string) string {
	return "huggingface/" + repo + "/" + commit + "/" + file
}

// RepoDir is the archive folder holding every archived revision of repo.
func RepoDir(repo string) string { return "huggingface/" + repo }

// ParseHFURL reads a model file URL on base's host (Hugging Face itself unless
// a test says otherwise). ok is false for anything else: another host, a
// dataset or a Space, or a path that isn't a file of a model repository.
func ParseHFURL(raw, base string) (f HFFile, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return HFFile{}, false
	}
	b, err := url.Parse(base)
	if err != nil || !strings.EqualFold(u.Host, b.Host) || u.Scheme != b.Scheme {
		return HFFile{}, false
	}
	segs := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(segs) < 5 || segs[2] != "resolve" {
		return HFFile{}, false
	}
	for i, s := range segs {
		v, err := url.PathUnescape(s)
		if err != nil || v == "" || v == "." || v == ".." {
			return HFFile{}, false
		}
		// A ref may be "refs/pr/1", escaped as one segment; no other part holds a slash.
		if i != 3 && strings.ContainsAny(v, `/\`) {
			return HFFile{}, false
		}
		segs[i] = v
	}
	if !repoPart.MatchString(segs[0]) || !repoPart.MatchString(segs[1]) {
		return HFFile{}, false
	}
	switch strings.ToLower(segs[0]) {
	case "datasets", "spaces", "api", "models":
		return HFFile{}, false
	}
	return HFFile{Repo: segs[0] + "/" + segs[1], Ref: segs[3], Path: strings.Join(segs[4:], "/")}, true
}

// Official is what Hugging Face says a file is.
type Official struct {
	Commit string `json:"commit"` // the revision the ref is at
	SHA256 string `json:"sha256"` // "" for a file not kept in LFS
	Size   int64  `json:"size"`
}

// ErrGone is a file, revision or repository Hugging Face says isn't there.
var ErrGone = errors.New("Hugging Face no longer has this file")

// HF reads Hugging Face. Base is DefaultHFBase unless a test points it elsewhere.
type HF struct {
	Base   string
	Client *http.Client
}

// NewHF is Hugging Face as the factory reaches it.
func NewHF() *HF {
	return &HF{
		Base: DefaultHFBase,
		Client: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (h *HF) request(ctx context.Context, method, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "llm-factory")
	return h.Client.Do(req)
}

// unquoteETag turns `W/"abc"` or `"abc"` into abc.
func unquoteETag(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "W/")
	return strings.ToLower(strings.Trim(v, `"`))
}

// gone reports whether an answer says the file, revision or repository isn't there.
func gone(resp *http.Response) bool {
	switch resp.Header.Get("X-Error-Code") {
	case "EntryNotFound", "RevisionNotFound", "RepoNotFound":
		return true
	}
	return resp.StatusCode == http.StatusNotFound
}

// Head asks Hugging Face what the file at rawURL is: the commit its ref is
// at, its SHA-256 and its size. An error wrapping ErrGone means Hugging Face
// says it isn't there; any other error means it couldn't say.
func (h *HF) Head(ctx context.Context, rawURL string) (Official, error) {
	target := rawURL
	for hops := 0; ; hops++ {
		resp, err := h.request(ctx, http.MethodHead, target)
		if err != nil {
			return Official{}, err
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		// A renamed repository: follow the relative redirect to its new name.
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
			if hops >= 5 {
				return Official{}, errors.New("Hugging Face redirected too many times")
			}
			next, err := url.Parse(target)
			if err != nil {
				return Official{}, err
			}
			ref, err := url.Parse(loc)
			if err != nil {
				return Official{}, err
			}
			target = next.ResolveReference(ref).String()
			continue
		}
		if gone(resp) {
			return Official{}, fmt.Errorf("%w (HTTP %d)", ErrGone, resp.StatusCode)
		}
		if resp.StatusCode >= 400 {
			return Official{}, fmt.Errorf("Hugging Face answered HTTP %d", resp.StatusCode)
		}
		commit := strings.ToLower(strings.TrimSpace(resp.Header.Get("X-Repo-Commit")))
		if !hexCommit.MatchString(commit) {
			return Official{}, errors.New("Hugging Face didn't say which revision the file is at")
		}
		o := Official{Commit: commit}
		if sum := unquoteETag(resp.Header.Get("X-Linked-Etag")); hexSHA256.MatchString(sum) {
			o.SHA256 = sum
		}
		if n, err := strconv.ParseInt(resp.Header.Get("X-Linked-Size"), 10, 64); err == nil && n >= 0 {
			o.Size = n
		} else if resp.StatusCode == http.StatusOK && resp.ContentLength >= 0 {
			o.Size = resp.ContentLength
		}
		return o, nil
	}
}

// ModelInfo is the part of a repository's model info the license rule reads.
type ModelInfo struct {
	Private     bool     `json:"private"`
	Gated       bool     `json:"gated"`
	GatedMode   string   `json:"gated_mode,omitempty"` // "auto" or "manual" when gated
	Licenses    []string `json:"licenses"`
	LicenseName string   `json:"license_name,omitempty"`
	LicenseLink string   `json:"license_link,omitempty"`
	Siblings    []string `json:"siblings"`
}

// Info reads a repository's model info at a commit.
func (h *HF) Info(ctx context.Context, repo, commit string) (ModelInfo, error) {
	org, name, ok := strings.Cut(repo, "/")
	if !ok {
		return ModelInfo{}, fmt.Errorf("%q isn't a repository", repo)
	}
	target := strings.TrimRight(h.Base, "/") + "/api/models/" + url.PathEscape(org) + "/" + url.PathEscape(name) + "/revision/" + url.PathEscape(commit)
	resp, err := h.request(ctx, http.MethodGet, target)
	if err != nil {
		return ModelInfo{}, err
	}
	defer resp.Body.Close()
	if gone(resp) || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ModelInfo{}, fmt.Errorf("%w (HTTP %d)", ErrGone, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return ModelInfo{}, fmt.Errorf("Hugging Face answered HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Private  bool            `json:"private"`
		Gated    json.RawMessage `json:"gated"`
		Tags     []string        `json:"tags"`
		CardData struct {
			License     json.RawMessage `json:"license"`
			LicenseName string          `json:"license_name"`
			LicenseLink string          `json:"license_link"`
		} `json:"cardData"`
		Siblings []struct {
			Name string `json:"rfilename"`
		} `json:"siblings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&raw); err != nil {
		return ModelInfo{}, fmt.Errorf("Hugging Face's model info couldn't be read: %w", err)
	}
	info := ModelInfo{Private: raw.Private, LicenseName: raw.CardData.LicenseName, LicenseLink: raw.CardData.LicenseLink}
	// gated is false, or the gate's mode ("auto", "manual"). Anything else is treated as gated.
	var gb bool
	if err := json.Unmarshal(raw.Gated, &gb); err == nil {
		info.Gated = gb
	} else {
		var gs string
		_ = json.Unmarshal(raw.Gated, &gs)
		info.Gated, info.GatedMode = len(raw.Gated) > 0 && string(raw.Gated) != "null", gs
	}
	seen := map[string]bool{}
	addLicense := func(l string) {
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" && !seen[l] {
			seen[l] = true
			info.Licenses = append(info.Licenses, l)
		}
	}
	var one string
	var many []string
	if json.Unmarshal(raw.CardData.License, &one) == nil {
		addLicense(one)
	} else if json.Unmarshal(raw.CardData.License, &many) == nil {
		for _, l := range many {
			addLicense(l)
		}
	}
	for _, t := range raw.Tags {
		if l, ok := strings.CutPrefix(t, "license:"); ok {
			addLicense(l)
		}
	}
	for _, s := range raw.Siblings {
		info.Siblings = append(info.Siblings, s.Name)
	}
	return info, nil
}

// Redistributable are the licenses the archive copies files under: each lets
// anybody redistribute the files, with the license and notices kept beside
// them. Custom licenses (llama*, gemma, "other", …) are left out for now.
var Redistributable = []string{
	"apache-2.0",
	"mit",
	"bsd-2-clause",
	"bsd-3-clause",
	"cc-by-4.0",
	"cc-by-sa-4.0",
	"cc0-1.0",
	// The OpenRAIL family: redistribution allowed, with the license's use
	// restrictions passed on (the license file and model card go with the files).
	"openrail",
	"openrail++",
	"creativeml-openrail-m",
	"bigscience-openrail-m",
	"bigcode-openrail-m",
}

// LicenseVerdict is the archive's rule: a repository is archived only when it
// is public, not gated, and every license it declares is on Redistributable.
// license names what it is under when ok; reason says why not otherwise.
func LicenseVerdict(info ModelInfo) (ok bool, license, reason string) {
	if info.Private {
		return false, "", "the repository is private"
	}
	if info.Gated {
		return false, "", "the repository is gated (Hugging Face asks people to accept its terms before downloading)"
	}
	if len(info.Licenses) == 0 {
		return false, "", "the repository declares no license"
	}
	allowed := map[string]bool{}
	for _, l := range Redistributable {
		allowed[l] = true
	}
	for _, l := range info.Licenses {
		if !allowed[l] {
			return false, "", fmt.Sprintf("its license (%s) isn't one the archive redistributes yet", l)
		}
	}
	return true, strings.Join(info.Licenses, ", "), ""
}

// NoticeFiles are the license and notice files at the top of a repository,
// which are archived beside its files: LICENSE*, LICENCE*, NOTICE*, COPYING*
// USE_POLICY* and the like (a use policy some licenses require passed on),
// and README.md (the model card, which states the license).
func NoticeFiles(siblings []string) []string {
	var out []string
	for _, s := range siblings {
		if strings.Contains(s, "/") {
			continue
		}
		l := strings.ToLower(s)
		if l == "readme.md" || strings.HasPrefix(l, "license") || strings.HasPrefix(l, "licence") ||
			strings.HasPrefix(l, "notice") || strings.HasPrefix(l, "copying") ||
			strings.HasPrefix(l, "use_policy") || strings.HasPrefix(l, "use-policy") ||
			strings.HasPrefix(l, "acceptable_use") || strings.HasPrefix(l, "acceptable-use") ||
			strings.HasPrefix(l, "responsible_use") || strings.HasPrefix(l, "responsible-use") {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// FileURL is the address of a file of repo at a commit.
func (h *HF) FileURL(repo, commit, file string) string {
	parts := strings.Split(file, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	org, name, _ := strings.Cut(repo, "/")
	return strings.TrimRight(h.Base, "/") + "/" + url.PathEscape(org) + "/" + url.PathEscape(name) + "/resolve/" + url.PathEscape(commit) + "/" + strings.Join(parts, "/")
}

// Fetch downloads a small file (a license, a model card) into w, at most max bytes.
func (h *HF) Fetch(ctx context.Context, target string, w io.Writer, max int64) error {
	for hops := 0; hops < 5; hops++ {
		resp, err := h.request(ctx, http.MethodGet, target)
		if err != nil {
			return err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			base, _ := url.Parse(target)
			next, err := url.Parse(loc)
			if err != nil || loc == "" {
				return errors.New("Hugging Face redirected nowhere")
			}
			target = base.ResolveReference(next).String()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		n, err := io.Copy(w, io.LimitReader(resp.Body, max+1))
		if err != nil {
			return err
		}
		if n > max {
			return fmt.Errorf("bigger than %d bytes", max)
		}
		return nil
	}
	return errors.New("Hugging Face redirected too many times")
}
