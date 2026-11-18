package filefreak

// Weights already on disk: a build only queues what it downloads, so files
// downloaded before the install had an upload key (or reused by later builds)
// would never reach the archive. ScanLocal finds them and queues the ones the
// archive may take, exactly as a fresh download would be queued: the file must
// be what Hugging Face publishes at its current revision (SHA-256), and the
// repository must pass the license rule. The license is checked before a file
// is hashed, and hashes are cached by size and modification time
// (config/filefreak-scan.json), so a periodic scan reads a file's bytes once.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LocalFile is one weight file on disk and where it came from.
type LocalFile struct {
	URL   string // the Hugging Face URL it was downloaded from
	Path  string // where it is on disk
	Label string // for logs and the queue ("Qwen3.5 9B")
}

// ScanResult counts what one scan did.
type ScanResult struct {
	Checked int `json:"checked"` // files on disk that were looked at
	Queued  int `json:"queued"`  // newly queued for upload
	Known   int `json:"known"`   // already queued, archived or skipped before
	Skipped int `json:"skipped"` // not archivable: license, revision, or no SHA-256
	Errors  int `json:"errors"`  // couldn't be checked this time (network, disk)
}

type hashEntry struct {
	Size    int64  `json:"size"`
	ModUnix int64  `json:"mod_unix"`
	SHA256  string `json:"sha256"`
}

type scanCache struct {
	path string
	mu   sync.Mutex
	m    map[string]hashEntry
	// licenses caches the license verdict per repo@commit for this process.
	licenses map[string]licenseVerdict
}

type licenseVerdict struct {
	ok      bool
	license string
	reason  string
}

func openScanCache(path string) *scanCache {
	c := &scanCache{path: path, m: map[string]hashEntry{}, licenses: map[string]licenseVerdict{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c.m) // a bad cache only costs a re-hash
	}
	return c
}

func (c *scanCache) get(path string, fi os.FileInfo) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[path]
	if !ok || e.Size != fi.Size() || e.ModUnix != fi.ModTime().Unix() {
		return "", false
	}
	return e.SHA256, true
}

func (c *scanCache) put(path string, fi os.FileInfo, sum string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[path] = hashEntry{Size: fi.Size(), ModUnix: fi.ModTime().Unix(), SHA256: sum}
	if c.path == "" {
		return
	}
	b, err := json.MarshalIndent(c.m, "", "  ")
	if err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, c.path)
	}
}

// has reports whether the queue knows the file in any state, so a scan never
// re-queues something archived, skipped, failed or waiting.
func (q *queue) has(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, o := range q.jobs {
		if o.key() == key {
			return true
		}
	}
	return false
}

// ScanLocal queues the files in files that the archive may take and doesn't
// have queued yet. It does nothing without an upload key. One scan at a time:
// a call while another runs returns at once with ok false.
func (a *Archive) ScanLocal(ctx context.Context, files []LocalFile) (res ScanResult, ok bool) {
	if a.store.Get().UploadKey == "" {
		return res, true
	}
	if !a.scanning.TryLock() {
		return res, false
	}
	defer a.scanning.Unlock()
	for _, lf := range files {
		if ctx.Err() != nil {
			break
		}
		fi, err := os.Stat(lf.Path)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
			continue
		}
		res.Checked++
		switch st, err := a.scanOne(ctx, lf, fi); {
		case err != nil:
			res.Errors++
			logf("scan: %s: %v", lf.Label, err)
		case st == "queued":
			res.Queued++
		case st == "known":
			res.Known++
		default:
			res.Skipped++
		}
	}
	if res.Queued > 0 {
		logf("scan: queued %d file(s) already on disk for the archive", res.Queued)
		a.kick()
	}
	return res, true
}

func (a *Archive) scanOne(ctx context.Context, lf LocalFile, fi os.FileInfo) (string, error) {
	f, ok := ParseHFURL(lf.URL, a.hf.Base)
	if !ok {
		return "skipped", nil // not from Hugging Face
	}
	hctx, cancel := context.WithTimeout(ctx, headTimeout)
	official, err := a.hf.Head(hctx, lf.URL)
	cancel()
	if errors.Is(err, ErrGone) {
		return "skipped", nil
	}
	if err != nil {
		return "", fmt.Errorf("ask Hugging Face: %w", err)
	}
	if official.SHA256 == "" {
		return "skipped", nil
	}
	key := Job{Repo: f.Repo, Revision: official.Commit, File: f.Path}.key()
	if a.queue.has(key) {
		return "known", nil
	}
	// A different size is another revision: it can't be checked against
	// Hugging Face's SHA-256, so it isn't hashed.
	if official.Size > 0 && official.Size != fi.Size() {
		return "skipped", nil
	}
	v, err := a.scanLicense(ctx, f.Repo, official.Commit)
	if err != nil {
		return "", err
	}
	if !v.ok {
		return "skipped", nil
	}
	sum, cached := a.scanCache.get(lf.Path, fi)
	if !cached {
		if sum, err = hashFile(ctx, lf.Path); err != nil {
			return "", err
		}
		a.scanCache.put(lf.Path, fi, sum)
	}
	if sum != official.SHA256 {
		return "skipped", nil // an older revision of the same name
	}
	added, err := a.queue.add(Job{
		Repo: f.Repo, Ref: f.Ref, Revision: official.Commit, File: f.Path, Label: lf.Label,
		LocalPath: lf.Path, SHA256: sum, Size: fi.Size(),
		SourceURL: a.hf.FileURL(f.Repo, official.Commit, f.Path),
	}, a.now())
	if err != nil {
		return "", err
	}
	if !added {
		return "known", nil
	}
	return "queued", nil
}

func (a *Archive) scanLicense(ctx context.Context, repo, commit string) (licenseVerdict, error) {
	c := a.scanCache
	c.mu.Lock()
	v, ok := c.licenses[repo+"@"+commit]
	c.mu.Unlock()
	if ok {
		return v, nil
	}
	ictx, cancel := context.WithTimeout(ctx, lookupTimeout)
	info, err := a.hf.Info(ictx, repo, commit)
	cancel()
	if errors.Is(err, ErrGone) {
		v = licenseVerdict{reason: "not public on Hugging Face"}
	} else if err != nil {
		return v, fmt.Errorf("read the license: %w", err)
	} else {
		v.ok, v.license, v.reason = LicenseVerdict(info)
		if v.ok {
			mctx, cancel := context.WithTimeout(ctx, lookupTimeout)
			ok, why, err := a.hf.MakerVerdict(mctx, info)
			cancel()
			if err != nil {
				return licenseVerdict{}, err
			}
			v.ok, v.reason = ok, why
		}
	}
	c.mu.Lock()
	c.licenses[repo+"@"+commit] = v
	c.mu.Unlock()
	return v, nil
}

// hashFile is a file's SHA-256, stopping when ctx ends.
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 4<<20)
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// StartScans scans first after first, then every interval, and soon after
// ScanSoon (a new upload key), until Close. files is called each time, so new
// weights are picked up.
func (a *Archive) StartScans(first, every time.Duration, files func() []LocalFile) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		t := time.NewTimer(first)
		defer t.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-t.C:
			case <-a.scanWake:
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
			}
			a.ScanLocal(a.ctx, files())
			t.Reset(every)
		}
	}()
}

// ScanSoon asks for a scan now (after a new upload key).
func (a *Archive) ScanSoon() {
	select {
	case a.scanWake <- struct{}{}:
	default:
	}
}

func scanCachePath(configDir string) string {
	if configDir == "" {
		return ""
	}
	return filepath.Join(configDir, "filefreak-scan.json")
}
