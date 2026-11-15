package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/seanbrill/local-llm/internal/builder"
)

// laneOrder is the refs of lane's queued jobs, in run order.
func laneOrder(q *buildQueue, lane string) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []string
	for _, j := range q.jobs {
		if j.lane == lane && j.status == jobQueued {
			out = append(out, j.ref)
		}
	}
	return out
}

func TestQueueMove(t *testing.T) {
	q := newBuildQueue()
	g := newGate()
	running := testJob("docker", "run", g.run)
	mustEnqueue(t, q, running)
	waitStarted(t, g)
	ids := map[string]string{}
	for _, ref := range []string{"a", "b", "c"} {
		j := testJob("docker", ref, newGate().run)
		mustEnqueue(t, q, j)
		ids[ref] = j.id
	}
	other := testJob("podman", "p", newGate().run) // starts at once on its own lane
	mustEnqueue(t, q, other)

	steps := []struct {
		id, to string
		want   []string
	}{
		{ids["c"], "up", []string{"a", "c", "b"}},
		{ids["c"], "top", []string{"c", "a", "b"}},
		{ids["c"], "up", []string{"c", "a", "b"}}, // already first
		{ids["c"], "bottom", []string{"a", "b", "c"}},
		{ids["a"], "down", []string{"b", "a", "c"}},
	}
	for _, s := range steps {
		if err := q.move(s.id, s.to); err != nil {
			t.Fatalf("move %s %s: %v", s.id, s.to, err)
		}
		if got := laneOrder(q, "docker"); !equal(got, s.want) {
			t.Fatalf("after %s: got %v, want %v", s.to, got, s.want)
		}
	}
	if err := q.move(running.id, "up"); err != errJobNotQueued {
		t.Fatalf("moving a running job: %v", err)
	}
	if err := q.move("nope", "up"); err != errJobNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	if err := q.move(ids["a"], "sideways"); err != errBadMove {
		t.Fatalf("bad direction: %v", err)
	}
	// When the running job ends, the lane's first queued job (b, after the
	// moves) starts next.
	g.release <- nil
	waitStatus(t, q, ids["b"], jobRunning)
	for _, id := range []string{ids["b"], ids["a"], ids["c"], other.id} {
		_, _ = q.cancelJob(id)
	}
	waitStatus(t, q, ids["b"], jobCanceled)
	waitStatus(t, q, other.id, jobCanceled)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestQueueRetry(t *testing.T) {
	q := newBuildQueue()
	calls := 0
	j := testJob("docker", "x:latest", func(ctx context.Context, log builder.LogFunc) error {
		calls++
		if calls == 1 {
			return context.Canceled // first attempt fails
		}
		return nil
	})
	mustEnqueue(t, q, j)
	waitStatus(t, q, j.id, jobError)

	nj, pos, err := q.retry(j.id)
	if err != nil || pos != 0 {
		t.Fatalf("retry: pos %d err %v", pos, err)
	}
	if nj.id == j.id || nj.ref != j.ref || nj.lane != j.lane {
		t.Fatalf("retry made %+v from %+v", nj, j)
	}
	waitStatus(t, q, nj.id, jobDone)
	if calls != 2 {
		t.Fatalf("run called %d times", calls)
	}
	if _, _, err := q.retry("nope"); err != errJobNotFound {
		t.Fatalf("unknown id: %v", err)
	}

	g := newGate()
	active := testJob("docker", "y:latest", g.run)
	mustEnqueue(t, q, active)
	waitStarted(t, g)
	if _, _, err := q.retry(active.id); err != errJobActive {
		t.Fatalf("retrying a running job: %v", err)
	}
	g.release <- nil
	waitStatus(t, q, active.id, jobDone)
}

func TestQueueWhileIdle(t *testing.T) {
	q := newBuildQueue()
	ran := false
	if !q.whileIdle(func() { ran = true }) || !ran {
		t.Fatal("an idle queue should run fn")
	}
	g := newGate()
	j := testJob("docker", "z", g.run)
	mustEnqueue(t, q, j)
	waitStarted(t, g)
	if q.whileIdle(func() { t.Fatal("ran while a build was running") }) {
		t.Fatal("whileIdle reported running fn during a build")
	}
	if !q.running() {
		t.Fatal("running() = false during a build")
	}
	if got := q.activeByLane()["docker"]; got != 1 {
		t.Fatalf("activeByLane = %d", got)
	}
	g.release <- nil
	waitStatus(t, q, j.id, jobDone)
}

func TestStorageUsage(t *testing.T) {
	dir := t.TempDir()
	b, err := builder.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, n int) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(b.ModelsDir, "a.gguf"), 3000)
	write(filepath.Join(b.ModelsDir, "snap", "x.safetensors"), 2000)
	write(filepath.Join(b.ModelsDir, ".gitkeep"), 0)
	write(filepath.Join(b.ImagesDir, "local-llm_a_latest.tar"), 1000)
	write(filepath.Join(b.BuildDir, "ctx-123", "Dockerfile"), 500)
	write(filepath.Join(b.BuildDir, "xfer-9.tar"), 700)
	write(filepath.Join(b.BuildDir, "keep.txt"), 100) // not a leftover pattern

	w := b.WeightsUsage()
	near := func(a, b float64) bool { return a-b < 1e-12 && b-a < 1e-12 }
	if w.Count != 2 || w.Items[0].Name != "a.gguf" || !near(w.GB, 5000/1e9) {
		t.Fatalf("weights: %+v", w)
	}
	if tars := b.TarsUsage(); tars.Count != 1 || !near(tars.GB, 1000/1e9) {
		t.Fatalf("tars: %+v", tars)
	}
	lo := b.LeftoversUsage()
	if lo.Count != 2 || !near(lo.GB, 1200/1e9) {
		t.Fatalf("leftovers: %+v", lo)
	}
	if removed := b.CleanStaleBuildDirs(); len(removed) != 2 {
		t.Fatalf("cleanup removed %v", removed)
	}
	if lo := b.LeftoversUsage(); lo.Count != 0 {
		t.Fatalf("leftovers after cleanup: %+v", lo)
	}
	if _, err := os.Stat(filepath.Join(b.BuildDir, "keep.txt")); err != nil {
		t.Fatal("cleanup removed a file it doesn't own")
	}
	if total, avail, err := builder.DiskSpace(dir); err != nil || total == 0 || avail > total {
		t.Fatalf("DiskSpace = %d, %d, %v", total, avail, err)
	}
}
