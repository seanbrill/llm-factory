package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
)

// gate is a build that runs until released (or its context ends).
type gate struct {
	started chan struct{}
	release chan error
}

func newGate() *gate { return &gate{started: make(chan struct{}), release: make(chan error, 1)} }

func (g *gate) run(ctx context.Context, log builder.LogFunc) error {
	close(g.started)
	log("building")
	select {
	case err := <-g.release:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func testJob(lane, ref string, run func(context.Context, builder.LogFunc) error) *buildJob {
	return &buildJob{kind: "model", lane: lane, ref: ref, name: ref,
		cfg: buildConfig{ModelID: ref, Engine: lane, Compute: "cpu"}, run: run}
}

func mustEnqueue(t *testing.T, q *buildQueue, j *buildJob) int {
	t.Helper()
	pos, err := q.enqueue(j)
	if err != nil {
		t.Fatal(err)
	}
	return pos
}

func waitStarted(t *testing.T, g *gate) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(3 * time.Second):
		t.Fatal("job never started")
	}
}

func notStarted(t *testing.T, g *gate, why string) {
	t.Helper()
	select {
	case <-g.started:
		t.Fatal(why)
	case <-time.After(40 * time.Millisecond):
	}
}

// waitStatus polls job id until it has status want.
func waitStatus(t *testing.T, q *buildQueue, id, want string) buildSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snap, ok := q.snapshot(id, 0)
		if ok && snap.Status == want {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s: status %q, want %q", id, snap.Status, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestQueueRunsALaneInOrder(t *testing.T) {
	q := newBuildQueue()
	g1, g2, g3 := newGate(), newGate(), newGate()
	j1, j2, j3 := testJob("docker", "a:1", g1.run), testJob("docker", "b:1", g2.run), testJob("docker", "c:1", g3.run)
	if p := mustEnqueue(t, q, j1); p != 0 {
		t.Errorf("first job position %d, want 0", p)
	}
	if p := mustEnqueue(t, q, j2); p != 1 {
		t.Errorf("second job position %d, want 1", p)
	}
	if p := mustEnqueue(t, q, j3); p != 2 {
		t.Errorf("third job position %d, want 2", p)
	}
	waitStarted(t, g1)
	notStarted(t, g2, "second job started while the first was building on the same engine")

	jobs := q.list()
	if jobs[0].Status != jobRunning || jobs[1].Status != jobQueued || jobs[1].Position != 1 || jobs[2].Position != 2 {
		t.Errorf("list = %+v", jobs)
	}

	g1.release <- nil
	waitStarted(t, g2)
	waitStatus(t, q, j1.id, jobDone)
	notStarted(t, g3, "third job jumped the queue")
	g2.release <- nil
	waitStarted(t, g3)
	g3.release <- nil
	waitStatus(t, q, j3.id, jobDone)

	// The worker retires when its lane is empty, and a new job gets a new one.
	deadline := time.Now().Add(3 * time.Second)
	for {
		q.mu.Lock()
		n := len(q.workers)
		q.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never retired")
		}
		time.Sleep(2 * time.Millisecond)
	}
	g4 := newGate()
	j4 := testJob("docker", "d:1", g4.run)
	if p := mustEnqueue(t, q, j4); p != 0 {
		t.Errorf("job on an idle lane: position %d", p)
	}
	waitStarted(t, g4)
	g4.release <- nil
	snap := waitStatus(t, q, j4.id, jobDone)
	if !slices.Equal(snap.Lines, []string{"building", "DONE"}) || snap.NextOffset != 2 || snap.Active {
		t.Errorf("snapshot = %+v", snap)
	}
}

func TestQueueEnginesBuildConcurrently(t *testing.T) {
	q := newBuildQueue()
	gd, gp := newGate(), newGate()
	mustEnqueue(t, q, testJob("docker", "a:1", gd.run))
	if p := mustEnqueue(t, q, testJob("podman", "b:1", gp.run)); p != 0 {
		t.Errorf("podman job waits behind docker: position %d", p)
	}
	// Both running at once: neither is released until both have started.
	waitStarted(t, gd)
	waitStarted(t, gp)
	gd.release <- nil
	gp.release <- nil
}

func TestQueueRefusesSameRefOnBothEngines(t *testing.T) {
	q := newBuildQueue()
	g := newGate()
	mustEnqueue(t, q, testJob("docker", "local-llm/x:latest", g.run))
	if _, err := q.enqueue(testJob("podman", "local-llm/x:latest", newGate().run)); err == nil || !strings.Contains(err.Error(), "already queued for docker") {
		t.Errorf("err = %v", err)
	}
	// The same engine just queues it (a rebuild after the first).
	g2 := newGate()
	if p := mustEnqueue(t, q, testJob("docker", "local-llm/x:latest", g2.run)); p != 1 {
		t.Errorf("same-engine rebuild position %d", p)
	}
	g.release <- nil
	waitStarted(t, g2)
	g2.release <- nil
}

func TestQueueCancelQueued(t *testing.T) {
	q := newBuildQueue()
	g1, g2 := newGate(), newGate()
	j1, j2 := testJob("docker", "a:1", g1.run), testJob("docker", "b:1", g2.run)
	mustEnqueue(t, q, j1)
	mustEnqueue(t, q, j2)
	waitStarted(t, g1)
	status, err := q.cancelJob(j2.id)
	if err != nil || status != jobCanceled {
		t.Fatalf("cancel queued = %q, %v", status, err)
	}
	snap, _ := q.snapshot(j2.id, 0)
	if snap.Active || snap.FinishedAt == 0 || snap.StartedAt != 0 {
		t.Errorf("canceled queued job = %+v", snap)
	}
	g1.release <- nil
	waitStatus(t, q, j1.id, jobDone)
	notStarted(t, g2, "a canceled job ran")
	if _, err := q.cancelJob(j2.id); !errors.Is(err, errJobFinished) {
		t.Errorf("second cancel: %v", err)
	}
	if _, err := q.cancelJob("nope"); !errors.Is(err, errJobNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestQueueCancelRunning(t *testing.T) {
	q := newBuildQueue()
	g := newGate()
	j := testJob("podman", "a:1", g.run)
	mustEnqueue(t, q, j)
	waitStarted(t, g)
	status, err := q.cancelJob(j.id)
	if err != nil || status != jobRunning {
		t.Fatalf("cancel running = %q, %v (it stays running until the worker stops it)", status, err)
	}
	snap := waitStatus(t, q, j.id, jobCanceled)
	if snap.Error != "" || !slices.Contains(snap.Lines, "Canceling …") || snap.LastLine != "CANCELED" {
		t.Errorf("canceled job = %+v", snap)
	}

	// A killed engine CLI fails with something that reads like an OOM kill; a
	// requested cancel still reports as canceled.
	q2 := newBuildQueue()
	started := make(chan struct{})
	j2 := testJob("podman", "b:1", func(ctx context.Context, log builder.LogFunc) error {
		close(started)
		<-ctx.Done()
		return errors.New("podman build failed: signal: killed")
	})
	mustEnqueue(t, q2, j2)
	<-started
	q2.cancelJob(j2.id)
	waitStatus(t, q2, j2.id, jobCanceled)
}

func TestQueueErrorsLeadWithTheHint(t *testing.T) {
	q := newBuildQueue()
	hint := "The build ran out of memory: the Podman machine (2.0 GB) can't run this many compiler jobs."
	j := testJob("podman", "a:1", func(ctx context.Context, log builder.LogFunc) error {
		log("c++: fatal error: Killed signal terminated program cc1plus")
		return &builder.HintError{Hint: hint, Err: errors.New("podman build failed: exit status 1")}
	})
	mustEnqueue(t, q, j)
	snap := waitStatus(t, q, j.id, jobError)
	want := []string{"c++: fatal error: Killed signal terminated program cc1plus", "ERROR: " + hint, "detail: podman build failed: exit status 1"}
	if snap.Error != hint || !slices.Equal(snap.Lines, want) {
		t.Errorf("error job: error %q lines %q", snap.Error, snap.Lines)
	}

	// A multi-line error (the memory preflight's) keeps its fix lines in the log
	// and its first line for the panel.
	j2 := testJob("podman", "b:1", func(ctx context.Context, log builder.LogFunc) error {
		return errors.New("Not enough memory to compile this vulkan image.\nTo fix it, run: podman machine set --memory 12288")
	})
	mustEnqueue(t, q, j2)
	snap = waitStatus(t, q, j2.id, jobError)
	if snap.Error != "Not enough memory to compile this vulkan image." || snap.LastLine != "To fix it, run: podman machine set --memory 12288" {
		t.Errorf("multi-line error: %q / %q", snap.Error, snap.Lines)
	}
}

func TestQueueTimeout(t *testing.T) {
	q := newBuildQueue()
	q.timeout = 30 * time.Millisecond
	g := newGate()
	j := testJob("docker", "a:1", g.run)
	mustEnqueue(t, q, j)
	snap := waitStatus(t, q, j.id, jobError)
	if snap.Error != "Timed out after 30ms." {
		t.Errorf("timeout error = %q", snap.Error)
	}
}

func TestQueueKeepsBoundedHistory(t *testing.T) {
	q := newBuildQueue()
	var last *buildJob
	for i := 0; i < keepFinishedJobs+5; i++ {
		last = testJob("docker", fmt.Sprintf("m%02d:1", i), func(context.Context, builder.LogFunc) error { return nil })
		mustEnqueue(t, q, last)
	}
	waitStatus(t, q, last.id, jobDone)
	jobs := q.list()
	if len(jobs) != keepFinishedJobs {
		t.Fatalf("kept %d jobs, want %d", len(jobs), keepFinishedJobs)
	}
	if jobs[0].Ref != "m05:1" || jobs[len(jobs)-1].Ref != fmt.Sprintf("m%02d:1", keepFinishedJobs+4) {
		t.Errorf("kept %s … %s, want the newest", jobs[0].Ref, jobs[len(jobs)-1].Ref)
	}

	// Active jobs are never pruned, however many finished ones there are.
	g := newGate()
	j := testJob("docker", "busy:1", g.run)
	mustEnqueue(t, q, j)
	waitStarted(t, g)
	if jobs := q.list(); jobs[len(jobs)-1].ID != j.id || len(jobs) != keepFinishedJobs+1 {
		t.Errorf("running job missing from %d jobs", len(jobs))
	}
	g.release <- nil
	waitStatus(t, q, j.id, jobDone)
	if n := len(q.list()); n != keepFinishedJobs {
		t.Errorf("after it finished: %d jobs", n)
	}
}

func TestQueueSnapshotOffsets(t *testing.T) {
	q := newBuildQueue()
	j := &buildJob{id: "x-1", lane: "docker", status: jobRunning, started: time.Now()}
	q.jobs = append(q.jobs, j)
	for i := 0; i <= maxJobLines; i++ { // one line past the cap
		q.appendLine(j, fmt.Sprintf("line %d", i))
	}
	total := maxJobLines + 1
	snap, ok := q.snapshot("x-1", 0)
	if !ok || snap.NextOffset != total || snap.LineCount != total {
		t.Fatalf("snapshot: ok %v next %d count %d", ok, snap.NextOffset, snap.LineCount)
	}
	// The oldest tenth (plus the overflow) was dropped; a reader at 0 resumes at
	// the oldest line still kept, and offsets stay absolute.
	drop := 1 + maxJobLines/10
	if len(snap.Lines) != total-drop || snap.Lines[0] != fmt.Sprintf("line %d", drop) {
		t.Errorf("trimmed snapshot: %d lines from %q", len(snap.Lines), snap.Lines[0])
	}
	if snap, _ := q.snapshot("x-1", total-1); !slices.Equal(snap.Lines, []string{fmt.Sprintf("line %d", total-1)}) {
		t.Errorf("tail = %q", snap.Lines)
	}
	if snap, _ := q.snapshot("x-1", total+50); len(snap.Lines) != 0 || snap.NextOffset != total {
		t.Errorf("past the end = %d lines, next %d", len(snap.Lines), snap.NextOffset)
	}
	if snap, _ := q.snapshot("x-1", -3); len(snap.Lines) != total-drop {
		t.Errorf("negative offset = %d lines", len(snap.Lines))
	}
	if _, ok := q.snapshot("nope", 0); ok {
		t.Error("unknown id found")
	}
}

func TestQueueWaitEstimate(t *testing.T) {
	q := newBuildQueue()
	now := time.Now()
	running := &buildJob{id: "1", lane: "docker", status: jobRunning, eta: 100, started: now.Add(-30 * time.Second)}
	otherLane := &buildJob{id: "2", lane: "podman", status: jobRunning, eta: 500, started: now}
	queued := &buildJob{id: "3", lane: "docker", status: jobQueued, eta: 50}
	finished := &buildJob{id: "4", lane: "docker", status: jobDone, eta: 999}
	mine := &buildJob{id: "5", lane: "docker", status: jobQueued, eta: 60}
	q.jobs = []*buildJob{running, otherLane, queued, finished, mine}

	if ahead, secs := q.wait(mine, now); ahead != 2 || secs != 70+50 {
		t.Errorf("wait = %d ahead, %ds; want 2, 120", ahead, secs)
	}
	if s := q.summary(mine, now); s.Position != 2 || s.WaitSecs != 120 || s.Engine != "docker" {
		t.Errorf("summary = %+v", s)
	}
	running.started = now.Add(-10 * time.Minute) // overran its ETA
	if _, secs := q.wait(mine, now); secs != 50 {
		t.Errorf("overrun = %ds, want 50", secs)
	}
	queued.eta = 0 // unknown: no estimate rather than a wrong one
	if ahead, secs := q.wait(mine, now); ahead != 2 || secs != 0 {
		t.Errorf("unknown eta = %d, %d", ahead, secs)
	}
	if s := q.summary(running, now); s.Position != 0 || s.ElapsedMs < 600000 {
		t.Errorf("running summary = %+v", s)
	}
}

func TestQueueCurrent(t *testing.T) {
	q := newBuildQueue()
	if q.current() != "" {
		t.Error("empty queue has a current job")
	}
	ga, gb := newGate(), newGate()
	a, b := testJob("docker", "a:1", ga.run), testJob("podman", "b:1", gb.run)
	mustEnqueue(t, q, a)
	waitStarted(t, ga)
	time.Sleep(2 * time.Millisecond)
	mustEnqueue(t, q, b)
	waitStarted(t, gb)
	if got := q.current(); got != a.id {
		t.Errorf("current = %s, want the first-started %s", got, a.id)
	}
	ga.release <- nil
	waitStatus(t, q, a.id, jobDone)
	if got := q.current(); got != b.id {
		t.Errorf("current = %s, want the running %s", got, b.id)
	}
	gb.release <- nil
	waitStatus(t, q, b.id, jobDone)
	if got := q.current(); got != b.id {
		t.Errorf("idle current = %s, want the most recent %s", got, b.id)
	}
}

func getJSON(t *testing.T, h http.HandlerFunc, method, target, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil && rec.Code < 400 {
		t.Fatalf("%s %s: %v: %s", method, target, err, rec.Body)
	}
	return rec.Code, out
}

func TestBuildQueueEndpoints(t *testing.T) {
	s := &Server{q: newBuildQueue()}

	// Idle: the legacy endpoint keeps its old empty shape.
	code, st := getJSON(t, s.handleBuildState, "GET", "/api/build/state", "")
	if code != 200 || st["active"] != false || st["status"] != "" || st["lines"] == nil {
		t.Errorf("idle legacy state = %d %v", code, st)
	}

	j := testJob("docker", "local-llm/a:latest", func(ctx context.Context, log builder.LogFunc) error {
		log("step 1")
		return nil
	})
	j.eta = 300
	rec := httptest.NewRecorder()
	pos := mustEnqueue(t, s.q, j)
	enqueueResponse(rec, j, pos)
	var enq map[string]any
	json.Unmarshal(rec.Body.Bytes(), &enq)
	if enq["id"] != j.id || enq["position"] != float64(0) || enq["status"] != "started" || enq["engine"] != "docker" {
		t.Errorf("enqueue response = %v", enq)
	}
	waitStatus(t, s.q, j.id, jobDone)

	_, list := getJSON(t, s.handleBuilds, "GET", "/api/builds", "")
	jobs, _ := list["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v", list)
	}
	row := jobs[0].(map[string]any)
	for _, k := range []string{"id", "kind", "status", "active", "ref", "model_id", "name", "engine", "compute", "config",
		"position", "wait_seconds", "created_at", "started_at", "finished_at", "elapsed_ms", "eta_seconds", "last_line", "line_count"} {
		if _, ok := row[k]; !ok {
			t.Errorf("job row lacks %q", k)
		}
	}
	if row["status"] != "done" || row["eta_seconds"] != float64(300) || row["last_line"] != "DONE" {
		t.Errorf("row = %v", row)
	}

	code, st = getJSON(t, s.handleBuildsState, "GET", "/api/builds/state?id="+j.id+"&offset=1", "")
	if code != 200 || st["next_offset"] != float64(2) || fmt.Sprint(st["lines"]) != "[DONE]" {
		t.Errorf("job state = %d %v", code, st)
	}
	// The legacy endpoint now reports the most recent job, in the old shape.
	_, st = getJSON(t, s.handleBuildState, "GET", "/api/build/state?offset=0", "")
	if st["status"] != "done" || st["active"] != false || st["ref"] != "local-llm/a:latest" || st["config"] == nil {
		t.Errorf("legacy state = %v", st)
	}
	if code, _ := getJSON(t, s.handleBuildsState, "GET", "/api/builds/state?id=nope", ""); code != 404 {
		t.Errorf("unknown job state: %d", code)
	}

	if code, _ := getJSON(t, s.handleBuildsCancel, "POST", "/api/builds/cancel", `{"id":"nope"}`); code != 404 {
		t.Errorf("cancel unknown: %d", code)
	}
	if code, out := getJSON(t, s.handleBuildsCancel, "POST", "/api/builds/cancel", `{"id":"`+j.id+`"}`); code != 409 || out["status"] != "done" {
		t.Errorf("cancel finished: %d %v", code, out)
	}
	g := newGate()
	j2 := testJob("docker", "local-llm/b:latest", g.run)
	mustEnqueue(t, s.q, j2)
	waitStarted(t, g)
	if code, out := getJSON(t, s.handleBuildsCancel, "POST", "/api/builds/cancel", `{"id":"`+j2.id+`"}`); code != 200 || out["status"] != "running" {
		t.Errorf("cancel running: %d %v", code, out)
	}
	waitStatus(t, s.q, j2.id, jobCanceled)
	req := httptest.NewRequest("GET", "/api/builds/cancel", nil)
	rec = httptest.NewRecorder()
	s.handleBuildsCancel(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET cancel: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	enqueueResponse(rec, j2, 2)
	json.Unmarshal(rec.Body.Bytes(), &enq)
	if enq["status"] != "queued" || enq["position"] != float64(2) {
		t.Errorf("queued response = %v", enq)
	}
}

func TestEngineFit(t *testing.T) {
	// gemma-4-12b's ~7.9 GB of weights.
	cases := []struct {
		label     string
		engine    string
		weights   float64
		mem       float64
		compute   string
		block     bool
		wantInMsg string
	}{
		{"vulkan on a 2 GB podman machine", "podman", 7.9, 2.0, "vulkan", true, "can't load onto the GPU"},
		{"vulkan, tight", "podman", 7.9, 9.0, "vulkan", false, "Tight fit"},
		{"vulkan fits", "podman", 7.9, 12.6, "vulkan", false, ""},
		{"cpu on small docker: mmap, so warn only", "docker", 7.9, 4.0, "cpu", false, "page its weights from disk"},
		{"cpu fits", "docker", 7.9, 18.8, "cpu", false, ""},
		{"cuda weights live in VRAM: warn only", "docker", 30, 16, "cuda", false, "page its weights"},
		{"unknown size", "podman", 0, 2.0, "vulkan", false, ""},
		{"unknown memory", "podman", 7.9, 0, "vulkan", false, ""},
	}
	for _, c := range cases {
		block, msg := engineFit("Gemma 4 12B", c.engine, c.weights, c.mem, c.compute)
		if block != c.block {
			t.Errorf("%s: block = %v, want %v (%s)", c.label, block, c.block, msg)
		}
		if c.wantInMsg == "" && msg != "" {
			t.Errorf("%s: unexpected message %q", c.label, msg)
		}
		if c.wantInMsg != "" && !strings.Contains(msg, c.wantInMsg) {
			t.Errorf("%s: message %q lacks %q", c.label, msg, c.wantInMsg)
		}
		if c.engine == "podman" && msg != "" && !strings.Contains(msg, "podman machine set --memory 12288") {
			t.Errorf("%s: no fix in %q", c.label, msg)
		}
	}
}
