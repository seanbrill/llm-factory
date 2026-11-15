package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seanbrill/local-llm/internal/ensemble"
)

func memberState(t *testing.T, mg *Manager, tool string) ensemble.MemberStatus {
	t.Helper()
	members, _ := mg.Status()
	for _, m := range members {
		if m.Tool == tool {
			return m
		}
	}
	t.Fatalf("no member %s", tool)
	return ensemble.MemberStatus{}
}

func TestStartOnDemand(t *testing.T) {
	te := newTestEns(t)
	mg := te.g.mg
	if s := memberState(t, mg, ensemble.ToolChat); s.State != ensemble.StateStopped {
		t.Fatalf("state before use = %s", s.State)
	}
	var said []string
	l, err := mg.Acquire(context.Background(), ensemble.ToolChat, func(s string) { said = append(said, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(said) == 0 || !strings.Contains(said[0], "Starting the chat member (qwen)") {
		t.Errorf("progress = %q", said)
	}
	s := memberState(t, mg, ensemble.ToolChat)
	if s.State != ensemble.StateReady || s.Port == 0 || s.Busy != 1 {
		t.Errorf("status = %+v", s)
	}
	l.Release()
	l.Release() // twice is harmless
	if s := memberState(t, mg, ensemble.ToolChat); s.Busy != 0 {
		t.Errorf("busy after release = %d", s.Busy)
	}
	if len(te.eng.runs) != 1 {
		t.Fatalf("runs = %d", len(te.eng.runs))
	}
	spec := te.eng.runs[0]
	if spec.Name != "ens-test-chat" || spec.Labels[ensemble.LabelMember] != "test" || spec.Labels[ensemble.LabelTool] != "chat" || spec.GPU != "" {
		t.Errorf("run spec = %+v", spec)
	}
	// A second request reuses it.
	l2, err := mg.Acquire(context.Background(), ensemble.ToolChat, nil)
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()
	if n := te.eng.runCount("local-llm/chat"); n != 1 {
		t.Errorf("started %d times", n)
	}
}

func TestConcurrentAcquireStartsOnce(t *testing.T) {
	te := newTestEns(t)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			l, err := te.g.mg.Acquire(context.Background(), ensemble.ToolGenerateImage, nil)
			if err == nil {
				l.Release()
			}
			errs <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := te.eng.runCount("local-llm/sd"); n != 1 {
		t.Errorf("started %d times", n)
	}
}

func TestHealthWaitsForLoading(t *testing.T) {
	var calls atomic.Int32
	te := newTestEns(t)
	te.image("local-llm/slow", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && calls.Add(1) < 4 {
			w.WriteHeader(http.StatusServiceUnavailable) // llama.cpp while loading
			return
		}
		healthOK(w)
	}))
	te.g.mg.members[ensemble.ToolChat].m.Image = "local-llm/slow"
	if err := te.g.mg.Start(context.Background(), ensemble.ToolChat); err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 4 {
		t.Errorf("ready after %d probes", calls.Load())
	}
}

func TestHealthWithoutHealthEndpoint(t *testing.T) {
	te := newTestEns(t) // the sd fake has no /health; / answers
	if err := te.g.mg.Start(context.Background(), ensemble.ToolGenerateImage); err != nil {
		t.Fatal(err)
	}
}

func TestStartFailureReportsLogs(t *testing.T) {
	te := newTestEns(t)
	te.eng.logs = "error: failed to load model 'model.gguf'"
	te.image("local-llm/broken", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	te.g.mg.members[ensemble.ToolChat].m.Image = "local-llm/broken"
	go func() { // the container exits while loading
		for !te.eng.running("ens-test-chat") {
			time.Sleep(5 * time.Millisecond)
		}
		te.eng.setState("ens-test-chat", "exited")
	}()
	err := te.g.mg.Start(context.Background(), ensemble.ToolChat)
	if err == nil || !strings.Contains(err.Error(), "its container exited") || !strings.Contains(err.Error(), "failed to load model") {
		t.Fatalf("err = %v", err)
	}
	s := memberState(t, te.g.mg, ensemble.ToolChat)
	if s.State != ensemble.StateFailed || !strings.Contains(s.Error, "failed to load model") {
		t.Errorf("status = %+v", s)
	}
	if te.eng.running("ens-test-chat") {
		t.Error("the failed container was left behind")
	}
	// The next request tries again.
	te.g.mg.members[ensemble.ToolChat].m.Image = "local-llm/chat"
	if err := te.g.mg.Start(context.Background(), ensemble.ToolChat); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestStartTimeout(t *testing.T) {
	te := newTestEns(t)
	te.image("local-llm/stuck", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	te.g.mg.members[ensemble.ToolChat].m.Image = "local-llm/stuck"
	te.g.mg.startTimeout = 200 * time.Millisecond
	err := te.g.mg.Start(context.Background(), ensemble.ToolChat)
	if err == nil || !strings.Contains(err.Error(), "didn't answer its health check within") || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v", err)
	}
}

func TestEngineRunError(t *testing.T) {
	te := newTestEns(t)
	te.eng.runErr["local-llm/chat"] = errors.New("Unable to find image 'local-llm/chat:latest' locally")
	err := te.g.mg.Start(context.Background(), ensemble.ToolChat)
	if err == nil || !strings.Contains(err.Error(), "the engine couldn't start local-llm/chat") || !strings.Contains(err.Error(), "Unable to find image") {
		t.Fatalf("err = %v", err)
	}
}

func TestEvictionLRU(t *testing.T) {
	te := newTestEns(t)
	mg := te.g.mg
	mg.budget = 10 // chat 6 + image 4 fit; video 8 needs both gone
	ctx := context.Background()
	for _, tool := range []string{ensemble.ToolChat, ensemble.ToolGenerateImage} {
		if err := mg.Start(ctx, tool); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond) // distinct last-use times
	}
	if _, used := mg.Status(); used != 10 {
		t.Fatalf("used = %v", used)
	}
	// Speech (1 GB) needs room: the least recently used (chat) goes.
	if err := mg.Start(ctx, ensemble.ToolSpeak); err != nil {
		t.Fatal(err)
	}
	if s := memberState(t, mg, ensemble.ToolChat); s.State != ensemble.StateStopped {
		t.Errorf("chat = %s, want evicted", s.State)
	}
	if s := memberState(t, mg, ensemble.ToolGenerateImage); s.State != ensemble.StateReady {
		t.Errorf("image = %s, want kept", s.State)
	}
	if te.eng.running("ens-test-chat") {
		t.Error("evicted container still running")
	}
	if _, used := mg.Status(); used > mg.budget {
		t.Errorf("used %v over budget %v", used, mg.budget)
	}
}

func TestEvictionSkipsBusyAndPinned(t *testing.T) {
	te := newTestEns(t)
	mg := te.g.mg
	mg.budget = 10
	mg.members[ensemble.ToolGenerateImage].m.Pinned = true
	ctx := context.Background()
	l, err := mg.Acquire(ctx, ensemble.ToolChat, nil) // busy
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := mg.Start(ctx, ensemble.ToolGenerateImage); err != nil {
		t.Fatal(err)
	}
	err = mg.Start(ctx, ensemble.ToolSpeak)
	if err == nil {
		t.Fatal("started over budget")
	}
	for _, want := range []string{"memory budget (10 GB) has no room", "chat (~6 GB, busy)", "image (~4 GB, kept warm)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v; want %q", err, want)
		}
	}
	if s := memberState(t, mg, ensemble.ToolSpeak); s.State != ensemble.StateStopped {
		t.Errorf("speak = %s", s.State)
	}
}

func TestMemberLargerThanBudget(t *testing.T) {
	te := newTestEns(t)
	te.g.mg.budget = 4
	err := te.g.mg.Start(context.Background(), ensemble.ToolGenerateVideo)
	if err == nil || !strings.Contains(err.Error(), "more than the ensemble's whole memory budget") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopAndStopAll(t *testing.T) {
	te := newTestEns(t)
	mg := te.g.mg
	ctx := context.Background()
	for _, tool := range []string{ensemble.ToolChat, ensemble.ToolSpeak} {
		if err := mg.Start(ctx, tool); err != nil {
			t.Fatal(err)
		}
	}
	if err := mg.Stop(ctx, ensemble.ToolChat); err != nil {
		t.Fatal(err)
	}
	if s := memberState(t, mg, ensemble.ToolChat); s.State != ensemble.StateStopped || te.eng.running("ens-test-chat") {
		t.Errorf("after stop: %+v", s)
	}
	if err := mg.Stop(ctx, "nope"); err == nil {
		t.Error("stopping an unknown member succeeded")
	}
	mg.StopAll(ctx)
	if te.eng.running("ens-test-speak") {
		t.Error("StopAll left a member running")
	}
}

func TestStopCancelsStart(t *testing.T) {
	te := newTestEns(t)
	te.image("local-llm/stuck", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	mg := te.g.mg
	mg.members[ensemble.ToolChat].m.Image = "local-llm/stuck"
	done := make(chan error, 1)
	go func() { done <- mg.Start(context.Background(), ensemble.ToolChat) }()
	for memberState(t, mg, ensemble.ToolChat).State != ensemble.StateStarting {
		time.Sleep(5 * time.Millisecond)
	}
	if err := mg.Stop(context.Background(), ensemble.ToolChat); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "stopped before it was ready") {
		t.Errorf("start = %v", err)
	}
	if s := memberState(t, mg, ensemble.ToolChat); s.State != ensemble.StateStopped {
		t.Errorf("state = %s", s.State)
	}
}

func TestWarmPinned(t *testing.T) {
	te := newTestEns(t)
	te.g.mg.members[ensemble.ToolSpeak].m.Pinned = true
	te.g.mg.WarmPinned(context.Background())
	if s := memberState(t, te.g.mg, ensemble.ToolSpeak); s.State != ensemble.StateReady || !s.Pinned {
		t.Errorf("pinned speak = %+v", s)
	}
	if s := memberState(t, te.g.mg, ensemble.ToolChat); s.State != ensemble.StateStopped {
		t.Errorf("chat = %s, want stopped", s.State)
	}
}

func TestAdoptAfterRestart(t *testing.T) {
	te := newTestEns(t)
	te.eng.adopt("test", ensemble.ToolChat, "local-llm/chat")     // ours: adopted
	te.eng.adopt("test", ensemble.ToolSpeak, "local-llm/whisper") // wrong image: removed
	te.eng.adopt("test", "retired", "local-llm/sd")               // no such member: removed
	mg := te.g.mg
	mg.Adopt(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for memberState(t, mg, ensemble.ToolChat).State != ensemble.StateReady {
		if time.Now().After(deadline) {
			t.Fatalf("chat = %+v", memberState(t, mg, ensemble.ToolChat))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if te.eng.running("ens-test-speak") || te.eng.running("ens-test-retired") {
		t.Error("stale members were kept")
	}
	if n := te.eng.runCount("local-llm/chat"); n != 0 {
		t.Errorf("the adopted member was started again (%d)", n)
	}
	l, err := mg.Acquire(context.Background(), ensemble.ToolChat, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
}

func TestMonitorNoticesDeadMember(t *testing.T) {
	te := newTestEns(t)
	mg := te.g.mg
	te.eng.logs = "Killed"
	if err := mg.Start(context.Background(), ensemble.ToolChat); err != nil {
		t.Fatal(err)
	}
	te.eng.images["local-llm/chat"].Close()    // the process died...
	te.eng.setState("ens-test-chat", "exited") // ...and the container with it
	mg.checkOnce(context.Background())
	s := memberState(t, mg, ensemble.ToolChat)
	if s.State != ensemble.StateFailed || !strings.Contains(s.Error, "stopped on its own") || !strings.Contains(s.Error, "Killed") {
		t.Errorf("status = %+v", s)
	}
	if te.eng.running("ens-test-chat") {
		t.Error("the dead container wasn't removed")
	}
}

func TestGPUFlags(t *testing.T) {
	te := newTestEns(t)
	mg := te.g.mg
	mg.compute = "cuda"
	mg.members[ensemble.ToolSpeak].m.Compute = "cpu"
	if g := mg.gpuFor(mg.members[ensemble.ToolChat].m); g != "cuda" {
		t.Errorf("chat gpu = %q (the ensemble's compute is the fallback)", g)
	}
	if g := mg.gpuFor(mg.members[ensemble.ToolSpeak].m); g != "" {
		t.Errorf("speak gpu = %q (its own compute wins)", g)
	}
}

func TestMissingMember(t *testing.T) {
	te := newTestEns(t, without(ensemble.ToolGenerateVideo))
	_, err := te.g.mg.Acquire(context.Background(), ensemble.ToolGenerateVideo, nil)
	var me *missingError
	if !errors.As(err, &me) || !strings.Contains(err.Error(), "no video member") {
		t.Fatalf("err = %v", err)
	}
}
