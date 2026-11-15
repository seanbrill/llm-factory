package filefreak

// The upload queue: files downloaded from Hugging Face, waiting to be added to
// the archive. One worker uploads them one at a time, in the background, and
// never holds up a build. config/filefreak-queue.json keeps the queue (and the
// id of an upload in progress), so a restart carries on where it stopped.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Job states.
const (
	StateQueued    = "queued"    // waiting for its turn (or its next try)
	StateChecking  = "checking"  // reading the license, looking in the archive
	StateUploading = "uploading" // sending the file
	StateDone      = "done"      // in the archive
	StateSkipped   = "skipped"   // not archived, and why (the license rule)
	StateFailed    = "failed"    // gave up after several tries; Retry starts again
)

const (
	keepFinished   = 50 // done, skipped and failed jobs kept for the System page
	maxAttempts    = 6
	maxNoticeBytes = 4 << 20 // a license or model card bigger than this isn't one
)

// Job is one file waiting for, or done with, the archive.
type Job struct {
	ID        string    `json:"id"`
	Repo      string    `json:"repo"`
	Ref       string    `json:"ref"`
	Revision  string    `json:"revision"`
	File      string    `json:"file"` // its path in the repository
	Label     string    `json:"label"`
	LocalPath string    `json:"local_path"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	SourceURL string    `json:"source_url"`
	License   string    `json:"license,omitempty"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	UploadID  string    `json:"upload_id,omitempty"`
	Sent      int64     `json:"sent"`
	Attempts  int       `json:"attempts"`
	NextTry   time.Time `json:"next_try,omitempty"`
	URL       string    `json:"url,omitempty"` // the archived file's public address
	QueuedAt  time.Time `json:"queued_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (j Job) finished() bool {
	return j.State == StateDone || j.State == StateSkipped || j.State == StateFailed
}

func (j Job) key() string { return j.Repo + "@" + j.Revision + ":" + j.File }

type queue struct {
	mu   sync.Mutex
	path string
	jobs []Job
}

func openQueue(path string) (*queue, error) {
	q := &queue{path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var v struct {
			Jobs []Job `json:"jobs"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
		}
		q.jobs = v.Jobs
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	// A restart stopped whatever was in flight: it goes back in line, keeping
	// its upload id so the upload carries on.
	for i := range q.jobs {
		if q.jobs[i].State == StateChecking || q.jobs[i].State == StateUploading {
			q.jobs[i].State = StateQueued
			q.jobs[i].NextTry = time.Time{}
		}
	}
	return q, nil
}

func (q *queue) saveLocked() error {
	return writePrivate(q.path, struct {
		Jobs []Job `json:"jobs"`
	}{q.jobs})
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// add queues a job unless the same file is already waiting or archived.
func (q *queue) add(j Job, now time.Time) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, o := range q.jobs {
		if o.key() == j.key() && (o.State == StateDone || !o.finished()) {
			return false, nil
		}
	}
	// A skipped or failed earlier try of the same file is replaced.
	kept := q.jobs[:0]
	for _, o := range q.jobs {
		if o.key() != j.key() {
			kept = append(kept, o)
		}
	}
	q.jobs = kept
	j.ID, j.State, j.QueuedAt, j.UpdatedAt = newID(), StateQueued, now, now
	q.jobs = append(q.jobs, j)
	q.trimLocked()
	return true, q.saveLocked()
}

// trimLocked drops the oldest finished jobs past keepFinished.
func (q *queue) trimLocked() {
	var finished []int
	for i, j := range q.jobs {
		if j.finished() {
			finished = append(finished, i)
		}
	}
	if len(finished) <= keepFinished {
		return
	}
	sort.Slice(finished, func(a, b int) bool { return q.jobs[finished[a]].UpdatedAt.Before(q.jobs[finished[b]].UpdatedAt) })
	drop := map[int]bool{}
	for _, i := range finished[:len(finished)-keepFinished] {
		drop[i] = true
	}
	kept := q.jobs[:0]
	for i, j := range q.jobs {
		if !drop[i] {
			kept = append(kept, j)
		}
	}
	q.jobs = kept
}

// next is the first queued job due now, or how long until one is (0: none waiting).
func (q *queue) next(now time.Time) (*Job, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var wait time.Duration
	for _, j := range q.jobs {
		if j.State != StateQueued {
			continue
		}
		if !j.NextTry.After(now) {
			c := j
			return &c, 0
		}
		if d := j.NextTry.Sub(now); wait == 0 || d < wait {
			wait = d
		}
	}
	return nil, wait
}

// update changes a job and saves the queue (save false: progress, kept in memory).
func (q *queue) update(id string, save bool, fn func(*Job)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.jobs {
		if q.jobs[i].ID == id {
			fn(&q.jobs[i])
			if save {
				q.trimLocked()
				if err := q.saveLocked(); err != nil {
					logf("couldn't save the upload queue: %v", err)
				}
			}
			return
		}
	}
}

func (q *queue) list() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]Job(nil), q.jobs...)
}

func (q *queue) get(id string) (Job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}

func (q *queue) remove(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, j := range q.jobs {
		if j.ID == id {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			if err := q.saveLocked(); err != nil {
				logf("couldn't save the upload queue: %v", err)
			}
			return true
		}
	}
	return false
}

// backoff is the wait before an upload's next try: 1, 4, 16, 64 minutes, then 4 hours.
func backoff(attempt int) time.Duration {
	d := time.Minute
	for i := 1; i < attempt && d < 4*time.Hour; i++ {
		d *= 4
	}
	if d > 4*time.Hour {
		d = 4 * time.Hour
	}
	return d
}

// ---------------------------------------------------------------------------
// The worker
// ---------------------------------------------------------------------------

// kick starts the worker when there is a key and work to do, or wakes it.
func (a *Archive) kick() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running || a.store.Get().UploadKey == "" || a.ctx.Err() != nil {
		return
	}
	if job, wait := a.queue.next(a.now()); job == nil && wait == 0 {
		return
	}
	a.running = true
	a.wg.Add(1)
	go a.work()
}

func (a *Archive) work() {
	defer a.wg.Done()
	for {
		job, wait := a.queue.next(a.now())
		a.mu.Lock()
		if a.store.Get().UploadKey == "" || a.ctx.Err() != nil || (job == nil && wait == 0) {
			a.running, a.current = false, ""
			a.mu.Unlock()
			return
		}
		if job != nil {
			a.current = job.ID
		}
		a.mu.Unlock()
		if job == nil {
			t := time.NewTimer(wait)
			select {
			case <-a.ctx.Done():
			case <-a.wake:
			case <-t.C:
			}
			t.Stop()
			continue
		}
		a.process(*job)
		a.mu.Lock()
		a.current = ""
		a.mu.Unlock()
	}
}

// outcome of one try at a job.
type outcome struct {
	state, reason, license, url string
	retry                       bool // try again later
}

// process tries one job once.
func (a *Archive) process(j Job) {
	key := a.store.Get().UploadKey
	a.queue.update(j.ID, true, func(x *Job) {
		x.State, x.Reason, x.Attempts, x.UpdatedAt = StateChecking, "", x.Attempts+1, a.now()
	})
	// The key's removal or replacement stops this try (stopCurrent).
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.stopJob = cancel
	a.mu.Unlock()
	out := a.archive(ctx, key, j)
	a.mu.Lock()
	a.stopJob = nil
	a.mu.Unlock()
	stopped := ctx.Err() != nil
	cancel()
	os.RemoveAll(filepath.Join(a.staging, j.ID))
	if stopped {
		// Shutting down, or the key changed: back in line, to carry on later.
		a.queue.update(j.ID, true, func(x *Job) { x.State, x.NextTry, x.Attempts = StateQueued, time.Time{}, x.Attempts-1 })
		return
	}
	a.queue.update(j.ID, true, func(x *Job) {
		x.UpdatedAt = a.now()
		if out.license != "" {
			x.License = out.license
		}
		if out.url != "" {
			x.URL = out.url
		}
		x.Reason = redact(out.reason, key)
		switch {
		case out.retry && x.Attempts < maxAttempts:
			x.State, x.NextTry = StateQueued, a.now().Add(a.retryIn(x.Attempts))
		case out.retry:
			x.State = StateFailed
		default:
			x.State, x.NextTry = out.state, time.Time{}
			if out.state == StateDone {
				x.UploadID, x.Sent = "", x.Size
			}
		}
	})
	if out.state == StateDone {
		logf("archived %s/%s at %s", j.Repo, j.File, short(j.Revision))
	} else if out.reason != "" {
		logf("%s/%s: %s", j.Repo, j.File, redact(out.reason, key))
	}
}

// archive does the work of one try: the license rule, the archive folder, a
// look in the archive, the license files, the file, and its provenance record.
func (a *Archive) archive(ctx context.Context, key string, j Job) outcome {
	if _, err := a.helper.Check(ctx); err != nil {
		return outcome{reason: err.Error(), retry: true}
	}
	info, err := a.hf.Info(ctx, j.Repo, j.Revision)
	if errors.Is(err, ErrGone) {
		return outcome{state: StateSkipped, reason: "Hugging Face doesn't show this repository publicly any more, so its license can't be checked"}
	}
	if err != nil {
		return outcome{reason: "couldn't read the license from Hugging Face: " + err.Error(), retry: true}
	}
	ok, license, why := LicenseVerdict(info)
	if !ok {
		return outcome{state: StateSkipped, reason: "Not archived: " + why + "."}
	}

	folder, err := a.keyFolder(ctx, key)
	if err != nil {
		return outcome{license: license, reason: "couldn't open the archive folder: " + err.Error(), retry: true}
	}
	dir := path.Join("huggingface", j.Repo, j.Revision)
	fileDir, name := path.Split(j.File)
	there, err := existsFile(ctx, a.helper, key, path.Join(folder.Name, dir, j.File))
	if err != nil {
		return outcome{license: license, reason: "couldn't look in the archive: " + err.Error(), retry: true}
	}
	replace := false
	if there != nil {
		if there.SHA256 == j.SHA256 {
			if err := a.uploadProvenance(ctx, key, folder, j, info, license, NoticeFiles(info.Siblings), false); err != nil {
				return outcome{license: license, reason: err.Error(), retry: true}
			}
			return outcome{state: StateDone, license: license, url: there.URL, reason: "Already in the archive."}
		}
		// A copy that isn't the file Hugging Face publishes: replaced with this one.
		replace = true
	}
	if fi, err := os.Stat(j.LocalPath); err != nil || fi.Size() != j.Size {
		return outcome{state: StateFailed, license: license, reason: "The downloaded file is gone or has changed since, so it can't be uploaded. Download the model again to queue it again."}
	}

	notices := NoticeFiles(info.Siblings)
	if err := a.uploadNotices(ctx, key, folder, j, notices); err != nil {
		return outcome{license: license, reason: err.Error(), retry: true}
	}

	a.queue.update(j.ID, true, func(x *Job) { x.State, x.UpdatedAt = StateUploading, a.now() })
	var lastSave time.Time
	file, err := uploadFile(ctx, a.helper, key, uploadRequest{
		File: j.LocalPath, Parent: folder.ID, Dir: path.Join(dir, fileDir), Name: name,
		SHA256: j.SHA256, Resume: j.UploadID, Replace: replace,
	}, func(ev Event) {
		switch ev.Event {
		case "begun":
			a.queue.update(j.ID, true, func(x *Job) { x.UploadID = ev.UploadID })
		case "restarted":
			a.queue.update(j.ID, true, func(x *Job) { x.Sent = 0 })
		case "progress":
			save := time.Since(lastSave) > time.Minute
			if save {
				lastSave = time.Now()
			}
			a.queue.update(j.ID, save, func(x *Job) { x.Sent = ev.Done })
		}
	})
	if err != nil {
		var he *HelperError
		if errors.As(err, &he) && he.UploadID != "" {
			a.queue.update(j.ID, true, func(x *Job) { x.UploadID = he.UploadID })
		}
		if errors.As(err, &he) && (he.Code == "sha256_mismatch" || he.Code == "not_verified") {
			return outcome{state: StateFailed, license: license, reason: "FileFreak refused the upload: " + he.Message}
		}
		return outcome{license: license, reason: "the upload stopped: " + err.Error(), retry: true}
	}
	if err := a.uploadProvenance(ctx, key, folder, j, info, license, notices, true); err != nil {
		return outcome{license: license, url: file.URL, reason: err.Error(), retry: true}
	}
	return outcome{state: StateDone, license: license, url: file.URL}
}

// keyFolder is the key's archive folder, found (or made) once per key.
func (a *Archive) keyFolder(ctx context.Context, key string) (KeyFolder, error) {
	cfg := a.store.Get()
	if cfg.Folder != nil && cfg.Folder.KeySum == keySum(key) && cfg.Folder.ID != "" {
		return *cfg.Folder, nil
	}
	return a.resolveFolder(ctx, key)
}

// resolveFolder asks FileFreak for the key's archive folder and keeps the answer.
func (a *Archive) resolveFolder(ctx context.Context, key string) (KeyFolder, error) {
	f, err := ensureFolder(ctx, a.helper, key, FolderName)
	if err != nil {
		return KeyFolder{}, err
	}
	f.KeySum, f.CheckedAt = keySum(key), a.now().UTC()
	err = a.store.Update(func(c *Config) error {
		if c.UploadKey != key {
			return errors.New("the upload key changed meanwhile")
		}
		c.Folder = &f
		return nil
	})
	return f, err
}

// uploadNotices puts the repository's license files and model card beside
// its files, unless they are there already.
func (a *Archive) uploadNotices(ctx context.Context, key string, folder KeyFolder, j Job, notices []string) error {
	dir := path.Join("huggingface", j.Repo, j.Revision)
	for _, n := range notices {
		there, err := existsFile(ctx, a.helper, key, path.Join(folder.Name, dir, n))
		if err != nil {
			return fmt.Errorf("couldn't look for %s in the archive: %w", n, err)
		}
		if there != nil {
			continue
		}
		local, err := a.stage(j.ID, n, func(w *bytes.Buffer) error {
			return a.hf.Fetch(ctx, a.hf.FileURL(j.Repo, j.Revision, n), w, maxNoticeBytes)
		})
		if err != nil {
			return fmt.Errorf("couldn't download %s from Hugging Face: %w", n, err)
		}
		_, err = uploadFile(ctx, a.helper, key, uploadRequest{File: local, Parent: folder.ID, Dir: dir, Name: n, Replace: true}, nil)
		os.Remove(local)
		if err != nil {
			return fmt.Errorf("couldn't upload %s: %w", n, err)
		}
	}
	return nil
}

// uploadProvenance writes the record readers check an archived copy against
// when Hugging Face can't be asked. It goes up after the file, so a record
// always has its file. With replace false, a record already there is kept.
func (a *Archive) uploadProvenance(ctx context.Context, key string, folder KeyFolder, j Job, info ModelInfo, license string, notices []string, replace bool) error {
	dir := path.Join("huggingface", j.Repo, j.Revision)
	fileDir, name := path.Split(j.File)
	recName := name + ".provenance.json"
	if !replace {
		there, err := existsFile(ctx, a.helper, key, path.Join(folder.Name, dir, fileDir, recName))
		if err != nil {
			return fmt.Errorf("couldn't look for the provenance record: %w", err)
		}
		if there != nil {
			return nil
		}
	}
	rec := Provenance{
		Schema: "llm-factory.archive.provenance/1", Source: "huggingface",
		Repo: j.Repo, Ref: j.Ref, Revision: j.Revision, File: j.File,
		SHA256: j.SHA256, Size: j.Size, SourceURL: j.SourceURL,
		License: license, LicenseName: info.LicenseName, LicenseLink: info.LicenseLink,
		Notices: notices, ArchivedAt: a.now().UTC().Format(time.RFC3339), ArchivedBy: "llm-factory " + a.version,
	}
	local, err := a.stage(j.ID, recName, func(w *bytes.Buffer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rec)
	})
	if err != nil {
		return fmt.Errorf("couldn't write the provenance record: %w", err)
	}
	defer os.Remove(local)
	if _, err := uploadFile(ctx, a.helper, key, uploadRequest{File: local, Parent: folder.ID, Dir: path.Join(dir, fileDir), Name: recName, Replace: true}, nil); err != nil {
		return fmt.Errorf("couldn't upload the provenance record: %w", err)
	}
	return nil
}

// stage writes a small file for upload into the staging dir.
func (a *Archive) stage(jobID, name string, fill func(*bytes.Buffer) error) (string, error) {
	var buf bytes.Buffer
	if err := fill(&buf); err != nil {
		return "", err
	}
	dir := filepath.Join(a.staging, jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, strings.ReplaceAll(name, "/", "_"))
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return p, nil
}
