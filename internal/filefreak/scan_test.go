package filefreak

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// scanArchive is an Archive with a key whose worker can't run (no Node), so
// queued jobs stay put while the test looks at them.
func scanArchive(t *testing.T, hf *fakeHF, key string) *Archive {
	t.Helper()
	a := testArchive(t, hf, &fakeRunner{checkErr: errors.New("no node")}, Config{UploadKey: key})
	a.retryIn = func(int) time.Duration { return time.Hour }
	a.scanCache = openScanCache(filepath.Join(t.TempDir(), "scan.json"))
	return a
}

func localWeight(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanQueuesWeightsAlreadyOnDisk(t *testing.T) {
	hf := newFakeHF(t)
	a := scanArchive(t, hf, upKey)
	p := localWeight(t, weights)
	files := []LocalFile{{URL: hf.url("model.gguf"), Path: p, Label: "Model"}, {URL: hf.url("gone.gguf"), Path: filepath.Join(t.TempDir(), "missing.gguf")}}

	res, ok := a.ScanLocal(context.Background(), files)
	if !ok || res.Checked != 1 || res.Queued != 1 {
		t.Fatalf("first scan: %+v ok=%v", res, ok)
	}
	jobs := a.queue.list()
	if len(jobs) != 1 || jobs[0].LocalPath != p || jobs[0].SHA256 != sumOf(weights) || jobs[0].Revision != testCommit || jobs[0].File != "model.gguf" || jobs[0].Label != "Model" {
		t.Fatalf("queue: %+v", jobs)
	}
	fi, _ := os.Stat(p)
	if sum, cached := a.scanCache.get(p, fi); !cached || sum != sumOf(weights) {
		t.Errorf("hash not cached: %q %v", sum, cached)
	}

	res, _ = a.ScanLocal(context.Background(), files)
	if res.Queued != 0 || res.Known != 1 || len(a.queue.list()) != 1 {
		t.Errorf("second scan: %+v, queue %d", res, len(a.queue.list()))
	}
}

func TestScanSkipsWhatTheArchiveCantTake(t *testing.T) {
	t.Run("license", func(t *testing.T) {
		hf := newFakeHF(t)
		hf.set(func(f *fakeHF) { f.info = `{"gated": false, "cardData": {"license": "gemma"}}` })
		a := scanArchive(t, hf, upKey)
		p := localWeight(t, weights)
		res, _ := a.ScanLocal(context.Background(), []LocalFile{{URL: hf.url("model.gguf"), Path: p}})
		fi, _ := os.Stat(p)
		if _, cached := a.scanCache.get(p, fi); res.Skipped != 1 || len(a.queue.list()) != 0 || cached {
			t.Errorf("%+v queue=%d hashed=%v", res, len(a.queue.list()), cached)
		}
	})
	t.Run("another revision", func(t *testing.T) {
		hf := newFakeHF(t)
		a := scanArchive(t, hf, upKey)
		other := append([]byte(nil), weights...)
		other[0] ^= 0xff // same size, different bytes
		res, _ := a.ScanLocal(context.Background(), []LocalFile{{URL: hf.url("model.gguf"), Path: localWeight(t, other)}})
		if res.Skipped != 1 || len(a.queue.list()) != 0 {
			t.Errorf("%+v queue=%d", res, len(a.queue.list()))
		}
	})
	t.Run("different size is not hashed", func(t *testing.T) {
		hf := newFakeHF(t)
		a := scanArchive(t, hf, upKey)
		p := localWeight(t, append(append([]byte(nil), weights...), 1))
		res, _ := a.ScanLocal(context.Background(), []LocalFile{{URL: hf.url("model.gguf"), Path: p}})
		fi, _ := os.Stat(p)
		if _, cached := a.scanCache.get(p, fi); res.Skipped != 1 || cached {
			t.Errorf("%+v hashed=%v", res, cached)
		}
	})
	t.Run("not from Hugging Face", func(t *testing.T) {
		hf := newFakeHF(t)
		a := scanArchive(t, hf, upKey)
		res, _ := a.ScanLocal(context.Background(), []LocalFile{{URL: "https://example.invalid/model.gguf", Path: localWeight(t, weights)}})
		if res.Skipped != 1 || len(a.queue.list()) != 0 {
			t.Errorf("%+v", res)
		}
	})
}

func TestScanNeedsAKey(t *testing.T) {
	hf := newFakeHF(t)
	a := scanArchive(t, hf, "")
	res, ok := a.ScanLocal(context.Background(), []LocalFile{{URL: hf.url("model.gguf"), Path: localWeight(t, weights)}})
	if !ok || res.Checked != 0 || hf.heads != 0 {
		t.Errorf("%+v ok=%v heads=%d", res, ok, hf.heads)
	}
}

func TestScanSoonWakesTheLoop(t *testing.T) {
	hf := newFakeHF(t)
	a := scanArchive(t, hf, upKey)
	p := localWeight(t, weights)
	a.StartScans(time.Hour, time.Hour, func() []LocalFile { return []LocalFile{{URL: hf.url("model.gguf"), Path: p}} })
	a.ScanSoon()
	deadline := time.Now().Add(5 * time.Second)
	for len(a.queue.list()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(a.queue.list()) != 1 {
		t.Fatal("ScanSoon didn't scan")
	}
}
