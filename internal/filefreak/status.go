package filefreak

// What the System page shows and changes: the settings, the key (as a hint
// only), the archive location, whether the helper can run, and the queue.

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

// Status is the archive as the System page shows it. It never holds the key.
type Status struct {
	ReadFirst bool `json:"read_from_archive"`
	Key       struct {
		Set  bool   `json:"set"`
		Hint string `json:"hint"`
	} `json:"key"`
	// Archive is the location readers use now; Source says where it came
	// from: env (FILEFREAK_ARCHIVE_URL), key (the upload key's own folder),
	// setting (entered on the System page), built-in, or none.
	Archive struct {
		URL    string `json:"url"`
		Source string `json:"source"`
	} `json:"archive"`
	Setting    string     `json:"archive_setting"`
	EnvURL     string     `json:"env_url"`
	DefaultURL string     `json:"default_url"`
	Folder     *KeyFolder `json:"key_folder"`
	Helper     struct {
		OK    bool   `json:"ok"`
		Node  string `json:"node"`
		Error string `json:"error"`
	} `json:"helper"`
	Worker struct {
		Active  bool   `json:"active"`
		Current string `json:"current"`
	} `json:"worker"`
	Queue    []JobView `json:"queue"`
	Licenses []string  `json:"licenses"`
}

// JobView is a queue entry for the UI (the local path stays on the server).
type JobView struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Repo      string     `json:"repo"`
	Revision  string     `json:"revision"`
	File      string     `json:"file"`
	Size      int64      `json:"size"`
	Sent      int64      `json:"sent"`
	State     string     `json:"state"`
	Reason    string     `json:"reason"`
	License   string     `json:"license"`
	Attempts  int        `json:"attempts"`
	NextTry   *time.Time `json:"next_try,omitempty"` // when a retry waits
	URL       string     `json:"url"`
	QueuedAt  time.Time  `json:"queued_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Status reads the archive's state for the System page.
func (a *Archive) Status(ctx context.Context) Status {
	cfg := a.store.Get()
	var s Status
	s.ReadFirst = cfg.readFirst()
	s.Key.Set, s.Key.Hint = cfg.UploadKey != "", KeyHint(cfg.UploadKey)
	s.Archive.URL, s.Archive.Source = a.readLink(cfg)
	s.Setting = cfg.ArchiveURL
	s.EnvURL = strings.TrimSpace(os.Getenv(ArchiveURLEnv))
	s.DefaultURL = DefaultArchiveURL
	if cfg.UploadKey != "" && cfg.Folder != nil && cfg.Folder.KeySum == keySum(cfg.UploadKey) {
		f := *cfg.Folder
		f.KeySum = ""
		s.Folder = &f
	}
	if node, err := a.helper.Check(ctx); err != nil {
		s.Helper.Node, s.Helper.Error = node, err.Error()
	} else {
		s.Helper.OK, s.Helper.Node = true, node
	}
	a.mu.Lock()
	s.Worker.Active, s.Worker.Current = a.running, a.current
	a.mu.Unlock()
	s.Queue = []JobView{}
	jobs := a.queue.list()
	// Newest first.
	for i := len(jobs) - 1; i >= 0; i-- {
		j := jobs[i]
		s.Queue = append(s.Queue, JobView{
			ID: j.ID, Label: j.Label, Repo: j.Repo, Revision: j.Revision, File: j.File, Size: j.Size, Sent: j.Sent,
			State: j.State, Reason: j.Reason, License: j.License, Attempts: j.Attempts, URL: j.URL,
			QueuedAt: j.QueuedAt, UpdatedAt: j.UpdatedAt,
		})
		if !j.NextTry.IsZero() {
			t := j.NextTry
			s.Queue[len(s.Queue)-1].NextTry = &t
		}
	}
	s.Licenses = append([]string(nil), Redistributable...)
	return s
}

// SetReadFirst turns "Download from the archive first" on or off.
func (a *Archive) SetReadFirst(on bool) error {
	return a.store.Update(func(c *Config) error {
		if on {
			c.ReadFirst = nil // the default
		} else {
			off := false
			c.ReadFirst = &off
		}
		return nil
	})
}

// SetArchiveURL sets (or, with "", clears) the archive location readers use.
func (a *Archive) SetArchiveURL(link string) error {
	link = strings.TrimSpace(link)
	if link != "" {
		if err := CheckArchiveURL(link); err != nil {
			return err
		}
	}
	return a.store.Update(func(c *Config) error {
		c.ArchiveURL = link
		return nil
	})
}

// SetKey saves an upload key, then finds or makes its public archive folder.
// The key is kept even when that fails (FileFreak unreachable, Node missing):
// the error is the folder's, and the worker tries again.
func (a *Archive) SetKey(ctx context.Context, key string) (folderErr error, err error) {
	key = strings.TrimSpace(key)
	if err := CheckKey(key); err != nil {
		return nil, err
	}
	a.stopCurrent()
	if err := a.store.Update(func(c *Config) error {
		if c.UploadKey != key {
			c.Folder = nil
		}
		c.UploadKey = key
		return nil
	}); err != nil {
		return nil, err
	}
	a.ScanSoon() // weights already on disk (scan.go)
	if _, err := a.helper.Check(ctx); err != nil {
		a.kick()
		return err, nil
	}
	_, ferr := a.resolveFolder(ctx, key)
	a.kick()
	return ferr, nil
}

// RemoveKey forgets the upload key: this install only reads from now on. An
// upload in progress stops; its queue entry waits for a key.
func (a *Archive) RemoveKey() error {
	a.stopCurrent()
	return a.store.Update(func(c *Config) error {
		c.UploadKey, c.Folder = "", nil
		return nil
	})
}

// CheckFolder looks the key's archive folder up again (after switching on the
// account's public page, say).
func (a *Archive) CheckFolder(ctx context.Context) error {
	key := a.store.Get().UploadKey
	if key == "" {
		return errors.New("set an upload key first")
	}
	if _, err := a.helper.Check(ctx); err != nil {
		return err
	}
	_, err := a.resolveFolder(ctx, key)
	a.kick()
	return err
}

// ErrNoJob is a queue entry that isn't there.
var ErrNoJob = errors.New("that upload isn't in the queue")

// Retry puts a skipped or failed entry (or one waiting for its next try) back
// at the front of the line.
func (a *Archive) Retry(id string) error {
	j, ok := a.queue.get(id)
	if !ok {
		return ErrNoJob
	}
	if j.State == StateChecking || j.State == StateUploading || j.State == StateDone {
		return errors.New("that one doesn't need a retry")
	}
	a.queue.update(id, true, func(x *Job) {
		x.State, x.Reason, x.Attempts, x.NextTry, x.UpdatedAt = StateQueued, "", 0, time.Time{}, a.now()
	})
	a.kick()
	return nil
}

// Remove takes an entry off the list. The one being uploaded can't be removed.
func (a *Archive) Remove(id string) error {
	a.mu.Lock()
	busy := a.current == id
	a.mu.Unlock()
	if busy {
		return errors.New("that file is being uploaded now: remove it once it stops")
	}
	if !a.queue.remove(id) {
		return ErrNoJob
	}
	return nil
}
