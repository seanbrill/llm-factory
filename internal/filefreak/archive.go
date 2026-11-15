// Package filefreak is the factory's FileFreak archive: a public FileFreak
// folder holding the raw open-source model files the factory downloads, so
// they stay available even if their official source goes away.
//
// Before every weight download the factory asks Hugging Face what the file is
// (its revision and SHA-256), and looks for it in the archive at
// huggingface/<org>/<repo>/<commit>/<file>. A copy there is used only when it
// is that SHA-256: the SDK checks the bytes against the SHA-256 the file was
// uploaded with, and this checks that against Hugging Face's. When Hugging Face
// can't be asked, an archived copy is used only when its provenance record and
// the SDK's check agree. Anything else downloads from Hugging Face as before.
//
// An install with an upload key also adds to the archive: after a download
// from Hugging Face, the file is queued, and a background worker uploads it
// (with the repository's license files and a provenance record) when the
// repository is public, not gated, and under a license on Redistributable.
// The queue is kept in config/filefreak-queue.json, so an upload cut short by
// a restart carries on where it stopped.
//
// The FileFreak SDK is Node; the work is done by tools/filefreak/ff.mjs, run
// as a child process (helper.go).
package filefreak

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultArchiveURL is the archive an install reads when nothing else names
// one: the link of the official archive's public folder,
// https://filefreak.link/public/<username>/<folder id>. Empty until that
// archive exists; FILEFREAK_ARCHIVE_URL or the System page's setting name one
// in the meantime.
const DefaultArchiveURL = ""

// FolderName is the archive's top folder in the upload key's drive.
const FolderName = "open-model-archive"

// ArchiveURLEnv overrides the archive location on one machine.
const ArchiveURLEnv = "FILEFREAK_ARCHIVE_URL"

// How long a step may take before it is given up and the download goes to
// Hugging Face instead.
const (
	headTimeout   = 20 * time.Second
	lookupTimeout = 45 * time.Second
	listTimeout   = 2 * time.Minute
)

var archiveLink = regexp.MustCompile(`^/public/[A-Za-z0-9_]{3,30}/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/?$`)

// CheckArchiveURL accepts a public folder's link, as the helper reads one.
func CheckArchiveURL(link string) error {
	u, err := url.Parse(link)
	local := err == nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")
	if err != nil || u.User != nil || u.RawQuery != "" || !(u.Scheme == "https" || (u.Scheme == "http" && local)) || !archiveLink.MatchString(u.Path) {
		return errors.New("use the archive's public folder link: https://filefreak.link/public/<username>/<folder id>")
	}
	return nil
}

// Options sets up an Archive.
type Options struct {
	ConfigDir  string // config/: filefreak.json and filefreak-queue.json
	StagingDir string // where license files and provenance records wait to be uploaded
	ToolsDir   string // tools/filefreak, which holds ff.mjs
	Version    string // the factory's version, for provenance records
}

// Archive is the factory's view of the FileFreak archive.
type Archive struct {
	store   *Store
	queue   *queue
	hf      *HF
	helper  runner
	staging string
	version string
	now     func() time.Time
	// retryIn is how long a failed upload waits before it is tried again.
	retryIn func(attempt int) time.Duration

	ctx    context.Context // the worker's; Close cancels it
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	running bool
	current string             // the job being worked on
	stopJob context.CancelFunc // stops the current try (the key changed)
	wake    chan struct{}
}

// stopCurrent stops the upload in progress, which goes back in line.
func (a *Archive) stopCurrent() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopJob != nil {
		a.stopJob()
	}
}

// Open loads the settings and the upload queue, and carries on any upload a
// restart cut short.
func Open(opts Options) (*Archive, error) {
	store, err := OpenStore(filepath.Join(opts.ConfigDir, "filefreak.json"))
	if err != nil {
		return nil, err
	}
	q, err := openQueue(filepath.Join(opts.ConfigDir, "filefreak-queue.json"))
	if err != nil {
		return nil, err
	}
	a := newArchive(store, q, NewHF(), NewNodeHelper(opts.ToolsDir), opts)
	a.kick()
	return a, nil
}

func newArchive(store *Store, q *queue, hf *HF, r runner, opts Options) *Archive {
	ctx, cancel := context.WithCancel(context.Background())
	return &Archive{
		store: store, queue: q, hf: hf, helper: r,
		staging: opts.StagingDir, version: opts.Version, now: time.Now,
		retryIn: backoff,
		ctx:     ctx, cancel: cancel,
		wake: make(chan struct{}, 1),
	}
}

// Close stops the upload worker (what it sent is kept, to carry on next time).
func (a *Archive) Close() {
	a.cancel()
	a.wg.Wait()
}

// readLink is the archive readers use now, and where that came from.
func (a *Archive) readLink(cfg Config) (link, source string) {
	if v := strings.TrimSpace(os.Getenv(ArchiveURLEnv)); v != "" {
		return v, "env"
	}
	if cfg.UploadKey != "" && cfg.Folder != nil && cfg.Folder.KeySum == keySum(cfg.UploadKey) && cfg.Folder.URL != "" {
		return cfg.Folder.URL, "key"
	}
	if cfg.ArchiveURL != "" {
		return cfg.ArchiveURL, "setting"
	}
	if DefaultArchiveURL != "" {
		return DefaultArchiveURL, "built-in"
	}
	return "", "none"
}

// fetchLog writes build-log lines about the archive.
type fetchLog func(string)

func (l fetchLog) say(format string, args ...any) { l(fmt.Sprintf(format, args...)) }

// Fetch runs before a weight download from rawURL into dest. It returns
// done when dest now holds the file from the archive. Otherwise the caller
// downloads from rawURL itself: want is the SHA-256 that download must match
// ("" when unknown), and downloaded (when not nil) is called once it has, to
// queue the file for the archive. It never fails a build: any problem with the
// archive is a line in the log and a download from Hugging Face.
func (a *Archive) Fetch(ctx context.Context, rawURL, dest, label string, logf func(string)) (done bool, want string, downloaded func(sha256 string, size int64)) {
	say := fetchLog(logf)
	f, ok := ParseHFURL(rawURL, a.hf.Base)
	if !ok {
		return false, "", nil
	}
	cfg := a.store.Get()
	link, _ := a.readLink(cfg)
	reading := cfg.readFirst() && link != ""
	uploading := cfg.UploadKey != ""
	if !reading && !uploading {
		return false, "", nil
	}
	if reading {
		if _, err := a.helper.Check(ctx); err != nil {
			say.say("FileFreak archive: not checked (%v). Downloading from Hugging Face.", err)
			reading = false
		}
	}

	hctx, cancel := context.WithTimeout(ctx, headTimeout)
	official, hfErr := a.hf.Head(hctx, rawURL)
	cancel()
	switch {
	case hfErr == nil && official.SHA256 != "":
		say.say("Hugging Face: %s is at revision %s, SHA-256 %s…, %s.", label, short(official.Commit), official.SHA256[:12], gbText(official.Size))
	case hfErr == nil:
		say.say("Hugging Face: %s is at revision %s (no SHA-256 published for it).", label, short(official.Commit))
	case errors.Is(hfErr, ErrGone):
		say.say("Hugging Face no longer has %s (%v).", label, hfErr)
	default:
		say.say("Couldn't ask Hugging Face about %s (%v).", label, hfErr)
	}

	inArchive := false
	if reading && ctx.Err() == nil {
		if hfErr == nil && official.SHA256 != "" {
			var used bool
			used, inArchive = a.fromArchive(ctx, link, f, official, dest, label, say)
			if used {
				return true, "", nil
			}
		} else if hfErr != nil {
			if a.fromProvenance(ctx, link, f, dest, label, say) {
				return true, "", nil
			}
		}
	}

	if hfErr != nil || official.SHA256 == "" {
		if uploading {
			say.say("FileFreak archive: %s won't be archived, because Hugging Face didn't confirm its revision and SHA-256.", label)
		}
		return false, "", nil
	}
	if !uploading || inArchive {
		return false, official.SHA256, nil
	}
	return false, official.SHA256, func(sum string, size int64) {
		if sum != official.SHA256 {
			return
		}
		added, err := a.queue.add(Job{
			Repo: f.Repo, Ref: f.Ref, Revision: official.Commit, File: f.Path, Label: label,
			LocalPath: dest, SHA256: sum, Size: size,
			SourceURL: a.hf.FileURL(f.Repo, official.Commit, f.Path),
		}, a.now())
		switch {
		case err != nil:
			say.say("FileFreak archive: couldn't queue %s (%v).", label, err)
		case !added:
			say.say("FileFreak archive: %s is already in the upload queue.", label)
		default:
			say.say("FileFreak archive: queued %s. The upload checks the model's license first and runs in the background (System page).", label)
			a.kick()
		}
	}
}

// fromArchive looks for the file Hugging Face described and downloads it if
// it is there. used: dest holds it. present: the archive has it (even if the
// download then failed), so it needn't be uploaded again.
func (a *Archive) fromArchive(ctx context.Context, link string, f HFFile, official Official, dest, label string, say fetchLog) (used, present bool) {
	path := ArchivePath(f.Repo, official.Commit, f.Path)
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	file, err := lookupFile(lctx, a.helper, link, path)
	cancel()
	switch {
	case err != nil:
		say.say("FileFreak archive: couldn't look %s up (%v). Downloading from Hugging Face.", label, err)
		return false, false
	case file == nil:
		say.say("FileFreak archive: %s isn't archived yet. Downloading from Hugging Face.", label)
		return false, false
	case file.SHA256 != official.SHA256 || (official.Size > 0 && file.Size != official.Size):
		say.say("FileFreak archive: the archived %s isn't the file Hugging Face publishes (SHA-256 %s…), so it isn't used. Downloading from Hugging Face.", label, prefix(file.SHA256))
		return false, false
	}
	say.say("Downloading %s from the FileFreak archive (%s) ...", label, gbText(file.Size))
	if err := a.download(ctx, link, file.ID, dest, official.SHA256, label, say); err != nil {
		say.say("FileFreak archive: the download didn't work (%v). Downloading from Hugging Face instead.", err)
		return false, true
	}
	say.say("Saved %s to models/ from the FileFreak archive: its SHA-256 matches the one Hugging Face publishes.", label)
	return true, true
}

// fromProvenance is the archive without Hugging Face: a copy is used only
// when its provenance record names this file at the ref asked for, with the
// SHA-256 and size FileFreak has for the copy, and the SDK's check of the
// bytes agrees.
func (a *Archive) fromProvenance(ctx context.Context, link string, f HFFile, dest, label string, say fetchLog) bool {
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	cands, err := listCandidates(lctx, a.helper, link, RepoDir(f.Repo), f.Path)
	cancel()
	if err != nil {
		say.say("FileFreak archive: couldn't look %s up (%v).", label, err)
		return false
	}
	for _, c := range cands {
		p := c.Provenance
		if p == nil || !strings.EqualFold(p.Repo, f.Repo) || p.File != f.Path || !(p.Ref == f.Ref || p.Revision == f.Ref) ||
			!hexSHA256.MatchString(p.SHA256) || p.SHA256 != c.File.SHA256 || p.Size != c.File.Size || p.Revision != c.Revision {
			continue
		}
		say.say("FileFreak archive: using the copy archived %s from revision %s (SHA-256 %s…), without Hugging Face's confirmation.",
			dayOf(p.ArchivedAt), short(p.Revision), p.SHA256[:12])
		say.say("Downloading %s from the FileFreak archive (%s) ...", label, gbText(c.File.Size))
		if err := a.download(ctx, link, c.File.ID, dest, p.SHA256, label, say); err != nil {
			say.say("FileFreak archive: that copy didn't work (%v).", err)
			continue
		}
		say.say("Saved %s to models/ from the FileFreak archive. Hugging Face couldn't confirm it, so it was checked against its provenance record instead: the record and FileFreak's checksum agree.", label)
		return true
	}
	say.say("FileFreak archive: no archived copy of %s with a matching provenance record.", label)
	return false
}

// download runs the helper's download into dest, with progress in the log.
// A failed one leaves nothing behind.
func (a *Archive) download(ctx context.Context, link, id, dest, sha, label string, say fetchLog) error {
	var last int64
	got, err := downloadFile(ctx, a.helper, link, id, dest, sha, func(ev Event) {
		switch ev.Event {
		case "progress":
			if ev.Done-last >= 100<<20 || ev.Done == ev.Total {
				last = ev.Done
				if ev.Total > 0 {
					say.say("  %.0f%% (%.2f / %.2f GB)", float64(ev.Done)/float64(ev.Total)*100, float64(ev.Done)/1e9, float64(ev.Total)/1e9)
				}
			}
		case "verifying":
			if ev.Done == ev.Total {
				say.say("  checked %s", gbText(ev.Total))
			}
		}
	})
	if err == nil && got.SHA256 != sha {
		err = fmt.Errorf("it came back as SHA-256 %s…", prefix(got.SHA256))
	}
	if err != nil {
		os.Remove(dest + ".part")
		if fi, statErr := os.Stat(dest); statErr == nil && !fi.IsDir() {
			os.Remove(dest)
		}
		return err
	}
	return nil
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func prefix(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	if sum == "" {
		return "none"
	}
	return sum
}

func gbText(n int64) string {
	if n <= 0 {
		return "size unknown"
	}
	return fmt.Sprintf("%.2f GB", float64(n)/1e9)
}

func dayOf(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return "on an unknown date"
}

// logf is the server log, for the worker. It never carries the key.
func logf(format string, args ...any) { log.Printf("filefreak: "+format, args...) }
