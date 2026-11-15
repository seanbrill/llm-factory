package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/ensemble"
)

// Manager runs an ensemble's members: it starts the one a request needs (as a
// sibling container through the host engine), waits for its health check,
// evicts the least recently used idle members to stay within the memory budget,
// adopts members still running from before a restart, and notices members that
// die. Every member is in one state (ensemble.State*); a start or stop runs in
// the background so status stays readable and other members stay usable.
type Manager struct {
	mu      sync.Mutex
	ensID   string
	budget  float64
	host    string // where members' published ports are reached
	compute string // fallback compute for members without their own
	eng     Engine
	members map[string]*member
	order   []string // manifest order

	probe        *http.Client
	startTimeout time.Duration // a start that isn't healthy by then fails
	pollEvery    time.Duration // health-check interval while starting
	now          func() time.Time
	logf         func(format string, args ...any)
}

type member struct {
	m       ensemble.Member
	name    string // container name
	state   string
	err     string
	since   time.Time
	port    int
	lastUse time.Time
	busy    int
	wait    chan struct{}      // closed when the current start or stop ends (nil when settled)
	cancel  context.CancelFunc // cancels the current start
	// gen counts finished starts; result is the last one's outcome (nil:
	// ready). A request waiting on a start reads the outcome of that start,
	// even if the member was stopped or evicted since.
	gen    int
	result error
}

func newManager(mani ensemble.Manifest, eng Engine, logf func(string, ...any)) *Manager {
	mg := &Manager{
		ensID: mani.ID, budget: mani.VRAMBudgetGB, host: mani.ModelHost, compute: mani.Compute, eng: eng,
		members:      map[string]*member{},
		probe:        &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}},
		startTimeout: 8 * time.Minute,
		pollEvery:    time.Second,
		now:          time.Now,
		logf:         logf,
	}
	for _, m := range mani.Members {
		if _, dup := mg.members[m.Tool]; dup {
			continue // one member per tool: the first wins
		}
		mg.members[m.Tool] = &member{m: m, name: ensemble.MemberContainer(mani.ID, m.Tool), state: ensemble.StateStopped, since: time.Now()}
		mg.order = append(mg.order, m.Tool)
	}
	return mg
}

// toolText names what a tool's member does, for status lines and errors.
func toolText(tool string) string {
	switch tool {
	case ensemble.ToolChat:
		return "chat"
	case ensemble.ToolSeeImage:
		return "vision"
	case ensemble.ToolTranscribe:
		return "speech-to-text"
	case ensemble.ToolSpeak:
		return "speech"
	case ensemble.ToolGenerateImage:
		return "image"
	case ensemble.ToolGenerateVideo:
		return "video"
	case ensemble.ToolConductor:
		return "conductor"
	}
	return tool
}

func memberLabel(m ensemble.Member) string {
	who := m.Model
	if who == "" {
		who = strings.TrimPrefix(m.Image, "local-llm/")
	}
	return fmt.Sprintf("the %s member (%s)", toolText(m.Tool), who)
}

// Has reports whether the ensemble has a member for tool.
func (mg *Manager) Has(tool string) bool {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	_, ok := mg.members[tool]
	return ok
}

// Member returns the member backing tool.
func (mg *Manager) Member(tool string) (ensemble.Member, bool) {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	st, ok := mg.members[tool]
	if !ok {
		return ensemble.Member{}, false
	}
	return st.m, true
}

// Lease is a ready member in use by one request. Release it when done: a
// leased member is never evicted.
type Lease struct {
	Member ensemble.Member
	base   string
	mg     *Manager
	st     *member
	once   sync.Once
}

// URL is the member's address for path.
func (l *Lease) URL(path string) string { return l.base + path }

// Release ends the lease.
func (l *Lease) Release() {
	l.once.Do(func() {
		l.mg.mu.Lock()
		l.st.busy--
		l.st.lastUse = l.mg.now()
		l.mg.mu.Unlock()
	})
}

// Acquire makes the tool's member ready (starting it, and evicting others to
// make room, when needed) and leases it. progress, if set, hears what it's
// waiting for.
func (mg *Manager) Acquire(ctx context.Context, tool string, progress func(string)) (*Lease, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := mg.ensure(ctx, tool, progress); err != nil {
			return nil, err
		}
		mg.mu.Lock()
		st := mg.members[tool]
		if st.state == ensemble.StateReady {
			st.busy++
			st.lastUse = mg.now()
			l := &Lease{Member: st.m, base: fmt.Sprintf("http://%s:%d", mg.host, st.port), mg: mg, st: st}
			mg.mu.Unlock()
			return l, nil
		}
		mg.mu.Unlock() // stopped again in between (evicted or died): go round
	}
	return nil, fmt.Errorf("%s keeps stopping before it can answer; see the ensemble's log", toolText(tool))
}

// ensure returns once the tool's member is ready, or why it can't be.
func (mg *Manager) ensure(ctx context.Context, tool string, progress func(string)) error {
	say := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	for {
		mg.mu.Lock()
		st, ok := mg.members[tool]
		if !ok {
			mg.mu.Unlock()
			return &missingError{tool}
		}
		switch st.state {
		case ensemble.StateReady:
			mg.mu.Unlock()
			return nil
		case ensemble.StateStopping:
			ch := st.wait
			mg.mu.Unlock()
			say(fmt.Sprintf("Waiting for %s to stop…", memberLabel(st.m)))
			if err := waitCh(ctx, ch); err != nil {
				return err
			}
			continue
		case ensemble.StateStarting:
		default: // stopped or failed: start it
			victims, err := mg.makeRoomLocked(st)
			if err != nil {
				mg.mu.Unlock()
				return err
			}
			mg.beginStartLocked(st, victims)
		}
		ch, gen := st.wait, st.gen
		label := memberLabel(st.m)
		mg.mu.Unlock()
		say(fmt.Sprintf("Starting %s…", label))
		if err := waitCh(ctx, ch); err != nil {
			return err
		}
		mg.mu.Lock()
		state, finished, result := st.state, st.gen != gen, st.result
		mg.mu.Unlock()
		switch {
		case finished && result != nil:
			return fmt.Errorf("%s didn't start: %v", label, result)
		case finished, state == ensemble.StateReady:
			return nil // ready (if it was evicted since, Acquire goes round)
		}
		// An adoption that didn't work out: start it fresh.
	}
}

type missingError struct{ tool string }

func (e *missingError) Error() string {
	return fmt.Sprintf("this ensemble has no %s member: add one on the factory's Ensembles page", toolText(e.tool))
}

func waitCh(ctx context.Context, ch chan struct{}) error {
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// usedLocked is the footprint of the members that are running or starting.
func (mg *Manager) usedLocked(except *member) float64 {
	var used float64
	for _, o := range mg.members {
		if o != except && (o.state == ensemble.StateReady || o.state == ensemble.StateStarting) {
			used += o.m.VRAMGB
		}
	}
	return used
}

// makeRoomLocked picks the least recently used idle members to stop so st fits
// the budget, and marks them stopping. Pinned and busy members stay.
func (mg *Manager) makeRoomLocked(st *member) ([]*member, error) {
	need := st.m.VRAMGB
	if mg.budget <= 0 || need <= 0 {
		return nil, nil
	}
	if need > mg.budget {
		return nil, fmt.Errorf("%s needs ~%.0f GB, more than the ensemble's whole memory budget (%.0f GB): raise the budget on the Ensembles page",
			memberLabel(st.m), need, mg.budget)
	}
	used := mg.usedLocked(st)
	if used+need <= mg.budget {
		return nil, nil
	}
	var idle []*member
	for _, o := range mg.members {
		if o != st && o.state == ensemble.StateReady && !o.m.Pinned && o.busy == 0 {
			idle = append(idle, o)
		}
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].lastUse.Before(idle[j].lastUse) })
	var victims []*member
	for _, o := range idle {
		if used+need <= mg.budget {
			break
		}
		victims = append(victims, o)
		used -= o.m.VRAMGB
	}
	if used+need > mg.budget {
		var held []string
		for _, tool := range mg.order {
			o := mg.members[tool]
			if o != st && (o.state == ensemble.StateReady || o.state == ensemble.StateStarting) {
				why := "busy"
				if o.m.Pinned {
					why = "kept warm"
				} else if o.state == ensemble.StateStarting {
					why = "starting"
				}
				held = append(held, fmt.Sprintf("%s (~%.0f GB, %s)", toolText(tool), o.m.VRAMGB, why))
			}
		}
		return nil, fmt.Errorf("the memory budget (%.0f GB) has no room for %s (~%.0f GB): %s. Try again when they finish, stop a member on the Ensembles page, or raise the budget",
			mg.budget, memberLabel(st.m), need, strings.Join(held, ", "))
	}
	for _, v := range victims {
		v.state = ensemble.StateStopping
		v.wait = make(chan struct{})
		v.since = mg.now()
	}
	return victims, nil
}

func (mg *Manager) beginStartLocked(st *member, victims []*member) {
	st.state = ensemble.StateStarting
	st.err = ""
	st.port = 0
	st.since = mg.now()
	st.wait = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), mg.startTimeout)
	st.cancel = cancel
	go mg.runStart(ctx, cancel, st, victims)
}

// runStart evicts the victims, then starts st and waits for its health check.
// It runs on its own context: a start goes on if the request that asked for it
// goes away (another may be waiting).
func (mg *Manager) runStart(ctx context.Context, cancel context.CancelFunc, st *member, victims []*member) {
	defer cancel()
	for _, v := range victims {
		mg.logf("evicting %s to make room for %s", toolText(v.m.Tool), toolText(st.m.Tool))
		mg.removeAndSettle(v, ensemble.StateStopped, "")
	}
	t0 := mg.now()
	port, err := mg.startContainer(ctx, st)
	mg.mu.Lock()
	if err != nil && errors.Is(ctx.Err(), context.Canceled) {
		err = errors.New("stopped before it was ready")
	}
	st.gen, st.result = st.gen+1, err
	if err != nil {
		st.state, st.err, st.port = ensemble.StateFailed, err.Error(), 0
		mg.logf("%s failed to start: %v", toolText(st.m.Tool), err)
	} else {
		st.state, st.port, st.lastUse = ensemble.StateReady, port, mg.now()
		mg.logf("%s is ready on :%d (%s)", toolText(st.m.Tool), port, mg.now().Sub(t0).Round(time.Second))
	}
	st.since = mg.now()
	st.cancel = nil
	close(st.wait)
	st.wait = nil
	mg.mu.Unlock()
}

// gpuFor is the GPU access a member's container gets: its own image's compute,
// else the ensemble's.
func (mg *Manager) gpuFor(m ensemble.Member) string {
	c := m.Compute
	if c == "" {
		c = mg.compute
	}
	if c == "cuda" || c == "vulkan" {
		return c
	}
	return ""
}

func (mg *Manager) startContainer(ctx context.Context, st *member) (int, error) {
	if err := mg.eng.Remove(ctx, st.name); err != nil { // a stale one from a crash
		return 0, fmt.Errorf("couldn't clear an old %s container: %v", st.name, err)
	}
	spec := RunSpec{
		Name: st.name, Image: st.m.Image, Port: st.m.Port, GPU: mg.gpuFor(st.m),
		Labels: map[string]string{ensemble.LabelMember: mg.ensID, ensemble.LabelTool: st.m.Tool},
	}
	mg.logf("starting %s: %s", toolText(st.m.Tool), st.m.Image)
	if err := mg.eng.Run(ctx, spec); err != nil {
		return 0, fmt.Errorf("the engine couldn't start %s: %v", st.m.Image, err)
	}
	port := st.m.Port
	if port == 0 {
		var err error
		for i := 0; i < 5; i++ {
			if port, err = mg.eng.HostPort(ctx, st.name); err == nil {
				break
			}
			if sleepCtx(ctx, 200*time.Millisecond) != nil {
				break
			}
		}
		if err != nil {
			mg.cleanup(st.name)
			return 0, fmt.Errorf("couldn't read its published port: %v", err)
		}
	}
	mg.mu.Lock()
	st.port = port // visible in status while it warms up
	mg.mu.Unlock()
	if err := mg.waitHealthy(ctx, st, port); err != nil {
		logs := mg.eng.Logs(context.Background(), st.name, 12)
		mg.cleanup(st.name)
		if logs != "" {
			err = fmt.Errorf("%v. Its last log lines:\n%s", err, logs)
		}
		return 0, err
	}
	return port, nil
}

func (mg *Manager) cleanup(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = mg.eng.Remove(ctx, name)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitHealthy polls the member until it answers, its container dies, or ctx ends.
func (mg *Manager) waitHealthy(ctx context.Context, st *member, port int) error {
	base := fmt.Sprintf("http://%s:%d", mg.host, port)
	last := "no answer yet"
	for n := 1; ; n++ {
		ok, why := mg.probeOnce(ctx, base)
		if ok {
			return nil
		}
		// A probe cut off by the start deadline says nothing about the
		// member: keep the last real answer ("loading") for the error.
		if ctx.Err() == nil {
			last = why
		}
		if n%4 == 0 { // now and then: is the container still there?
			if state, err := mg.eng.State(ctx, st.name); err == nil && state != "running" && state != "created" && state != "restarting" {
				if state == "" {
					return errors.New("its container disappeared")
				}
				return fmt.Errorf("its container %s", state)
			}
		}
		if err := sleepCtx(ctx, mg.pollEvery); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("it didn't answer its health check within %s (last: %s)", mg.startTimeout, last)
			}
			return err
		}
	}
}

// probeOnce asks a member whether it's ready. /health is 200 when it is
// (llama.cpp, whisper.cpp, audio.cpp and the factory's gates); 502/503 mean
// it's still loading; a server without /health (stable-diffusion.cpp) that
// answers / at all is up, since it only listens once its model is loaded.
func (mg *Manager) probeOnce(ctx context.Context, base string) (bool, string) {
	get := func(path string) (int, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		resp, err := mg.probe.Do(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	code, err := get("/health")
	switch {
	case err != nil:
		return false, "not listening yet"
	case code == http.StatusOK:
		return true, ""
	case code == http.StatusNotFound || code == http.StatusMethodNotAllowed:
		if c2, err := get("/"); err == nil && c2 < 500 {
			return true, ""
		}
		return false, "no health check and / doesn't answer"
	default:
		return false, fmt.Sprintf("health check says %d (loading)", code)
	}
}

// removeAndSettle removes a member's container and leaves it in state.
func (mg *Manager) removeAndSettle(st *member, state, why string) {
	mg.cleanup(st.name)
	mg.mu.Lock()
	st.state, st.err, st.port, st.since = state, why, 0, mg.now()
	if st.wait != nil {
		close(st.wait)
		st.wait = nil
	}
	mg.mu.Unlock()
}

// Start makes a member ready (the factory's "Start" button, and keep-warm).
func (mg *Manager) Start(ctx context.Context, tool string) error {
	return mg.ensure(ctx, tool, nil)
}

// Stop stops a member (cancelling a start in progress) and frees its memory.
func (mg *Manager) Stop(ctx context.Context, tool string) error {
	for {
		mg.mu.Lock()
		st, ok := mg.members[tool]
		if !ok {
			mg.mu.Unlock()
			return &missingError{tool}
		}
		switch st.state {
		case ensemble.StateStarting:
			ch := st.wait
			if st.cancel != nil {
				st.cancel()
			}
			mg.mu.Unlock()
			if err := waitCh(ctx, ch); err != nil {
				return err
			}
			continue // now failed ("stopped before it was ready"): settle below
		case ensemble.StateStopping:
			ch := st.wait
			mg.mu.Unlock()
			return waitCh(ctx, ch)
		}
		st.state = ensemble.StateStopping
		st.wait = make(chan struct{})
		st.since = mg.now()
		mg.mu.Unlock()
		mg.logf("stopping %s", toolText(tool))
		mg.removeAndSettle(st, ensemble.StateStopped, "")
		return nil
	}
}

// StopAll stops every member, for the Conductor's shutdown.
func (mg *Manager) StopAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, tool := range mg.order {
		wg.Add(1)
		go func(tool string) {
			defer wg.Done()
			_ = mg.Stop(ctx, tool)
		}(tool)
	}
	wg.Wait()
}

// Adopt takes over members still running from before the Conductor restarted
// (it checks each one's health) and removes stale or foreign ones.
func (mg *Manager) Adopt(ctx context.Context) {
	cs, err := mg.eng.List(ctx, ensemble.LabelMember+"="+mg.ensID)
	if err != nil {
		mg.logf("can't list members left from before a restart: %v", err)
		return
	}
	for _, c := range cs {
		mg.mu.Lock()
		st, ok := mg.members[c.Labels[ensemble.LabelTool]]
		keep := ok && c.Name == st.name && c.State == "running" && sameImage(c.Image, st.m.Image) && st.state == ensemble.StateStopped
		if keep {
			st.state, st.err, st.since = ensemble.StateStarting, "", mg.now()
			st.wait = make(chan struct{})
		}
		mg.mu.Unlock()
		if !keep {
			mg.logf("removing %s (left over, %s)", c.Name, c.State)
			mg.cleanup(c.Name)
			continue
		}
		go mg.adoptOne(st)
	}
}

func sameImage(running, want string) bool {
	norm := func(s string) string {
		s = strings.TrimPrefix(s, "docker.io/")
		s = strings.TrimPrefix(s, "localhost/")
		if !strings.Contains(s[strings.LastIndexByte(s, '/')+1:], ":") {
			s += ":latest"
		}
		return s
	}
	return norm(running) == norm(want)
}

func (mg *Manager) adoptOne(st *member) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	port := st.m.Port
	var err error
	if port == 0 {
		port, err = mg.eng.HostPort(ctx, st.name)
	}
	if err == nil {
		err = mg.waitHealthy(ctx, st, port)
	}
	if err != nil {
		mg.logf("couldn't adopt %s (%v); it starts fresh when needed", st.name, err)
		mg.removeAndSettle(st, ensemble.StateStopped, "")
		return
	}
	mg.mu.Lock()
	st.state, st.port, st.since, st.lastUse = ensemble.StateReady, port, mg.now(), mg.now()
	st.gen, st.result = st.gen+1, nil
	close(st.wait)
	st.wait = nil
	mg.mu.Unlock()
	mg.logf("adopted %s, still running on :%d", toolText(st.m.Tool), port)
}

// WarmPinned starts the keep-warm members, one at a time.
func (mg *Manager) WarmPinned(ctx context.Context) {
	for _, tool := range mg.order {
		mg.mu.Lock()
		pinned := mg.members[tool].m.Pinned
		mg.mu.Unlock()
		if !pinned {
			continue
		}
		if err := mg.ensure(ctx, tool, nil); err != nil {
			mg.logf("keep-warm %s: %v", toolText(tool), err)
		}
	}
}

// Monitor notices ready members whose container died, so status says so and
// the next request starts them again.
func (mg *Manager) Monitor(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		mg.checkOnce(ctx)
	}
}

func (mg *Manager) checkOnce(ctx context.Context) {
	type probe struct {
		st   *member
		base string
	}
	var ps []probe
	mg.mu.Lock()
	for _, tool := range mg.order {
		st := mg.members[tool]
		if st.state == ensemble.StateReady && st.busy == 0 {
			ps = append(ps, probe{st, fmt.Sprintf("http://%s:%d", mg.host, st.port)})
		}
	}
	mg.mu.Unlock()
	for _, p := range ps {
		if ok, _ := mg.probeOnce(ctx, p.base); ok {
			continue
		}
		state, err := mg.eng.State(ctx, p.st.name)
		if err != nil || state == "running" {
			continue // the engine didn't answer, or it's alive but busy
		}
		why := "its container is gone"
		if state != "" {
			why = "its container " + state
		}
		if logs := mg.eng.Logs(ctx, p.st.name, 6); logs != "" {
			why += ". Its last log lines:\n" + logs
		}
		mg.mu.Lock()
		if p.st.state == ensemble.StateReady && p.st.busy == 0 {
			p.st.state, p.st.err, p.st.port, p.st.since = ensemble.StateFailed, "it stopped on its own: "+why, 0, mg.now()
			mg.logf("%s stopped on its own (%s)", toolText(p.st.m.Tool), state)
		}
		mg.mu.Unlock()
		mg.cleanup(p.st.name)
	}
}

// Status reports every member and the memory in use.
func (mg *Manager) Status() ([]ensemble.MemberStatus, float64) {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	out := make([]ensemble.MemberStatus, 0, len(mg.order))
	for _, tool := range mg.order {
		st := mg.members[tool]
		ms := ensemble.MemberStatus{
			Tool: tool, Modality: st.m.Modality, Image: st.m.Image, Model: st.m.Model,
			State: st.state, Error: st.err, Since: st.since.UnixMilli(), Port: st.port,
			MemGB: st.m.VRAMGB, Pinned: st.m.Pinned, Busy: st.busy, VoiceClone: st.m.VoiceClone,
		}
		if !st.lastUse.IsZero() {
			ms.LastUsed = st.lastUse.UnixMilli()
		}
		out = append(out, ms)
	}
	return out, mg.usedLocked(nil)
}
