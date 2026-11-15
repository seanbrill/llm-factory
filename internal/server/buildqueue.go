package server

// Build queue. POST /api/build adds a job instead of refusing while another
// build runs. One worker per engine runs that engine's jobs in order — so the
// second llama.cpp image reuses the first one's cached compile layer and only
// adds its weights — while different engines build at the same time. Every job
// keeps its own log, status and timings, so the UI can stream any of them and
// reattach to running/queued jobs after a refresh.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/builder"
)

// Job statuses.
const (
	jobQueued   = "queued"
	jobRunning  = "running"
	jobDone     = "done"
	jobError    = "error"
	jobCanceled = "canceled"
)

const (
	keepFinishedJobs = 30            // finished jobs kept for the queue panel
	maxJobLines      = 20000         // log lines kept per job (oldest trimmed first)
	jobTimeout       = 2 * time.Hour // so a wedged build can't hold its engine forever
)

var (
	errJobNotFound = errors.New("no such build")
	errJobFinished = errors.New("that build already finished")
)

// buildConfig is the form config of a build, kept so a refreshed UI can restore
// the build form (and show what's building).
type buildConfig struct {
	ModelID      string  `json:"model_id"`
	ImageName    string  `json:"image_name"`
	Tag          string  `json:"tag"`
	Engine       string  `json:"engine"`
	Compute      string  `json:"compute"`
	SystemPrompt string  `json:"system_prompt"`
	InjectMode   string  `json:"inject_mode"`
	CtxSize      int     `json:"ctx_size"`
	MemoryGB     float64 `json:"memory_gb"`
	Route        string  `json:"route"`
	Autostart    bool    `json:"autostart"`
	// Harness is the agent harness baked into the image (nil = none).
	Harness *agent.Config `json:"harness,omitempty"`
}

// buildJob is one queued, running or finished image build.
type buildJob struct {
	id   string
	kind string // "model" | "ensemble"
	lane string // engine whose worker runs it: the engine the image is for
	ref  string
	name string // model / ensemble display name
	cfg  buildConfig
	eta  int // expected seconds (the last build of this compute; 0 = unknown)
	run  func(ctx context.Context, log builder.LogFunc) error

	// Guarded by buildQueue.mu.
	status   string
	created  time.Time
	started  time.Time
	finished time.Time
	lines    []string
	trimmed  int    // lines dropped from the front; offsets stay absolute
	errMsg   string // the lead error line, for the queue panel
	cancel   context.CancelFunc
	canceled bool   // cancel requested while running
	buildEng string // engine compiling it, once the builder has chosen ("" = not yet / not a compile)
}

func (j *buildJob) active() bool { return j.status == jobQueued || j.status == jobRunning }

// buildQueue holds the jobs (submission order: active ones plus recent history)
// and one worker goroutine per lane that has work.
type buildQueue struct {
	mu       sync.Mutex
	jobs     []*buildJob
	workers  map[string]bool
	seq      int
	idPrefix string // distinguishes ids across factory restarts
	timeout  time.Duration
}

func newBuildQueue() *buildQueue {
	return &buildQueue{
		workers:  map[string]bool{},
		idPrefix: strconv.FormatInt(time.Now().Unix(), 36),
		timeout:  jobTimeout,
	}
}

// enqueue adds j and returns how many unfinished jobs are ahead of it in its
// engine's lane (0 = it starts now). It refuses a ref that's already queued or
// building for the other engine — both would export the same images/<ref>.tar.
func (q *buildQueue) enqueue(j *buildJob) (position int, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, o := range q.jobs {
		if o.active() && o.ref == j.ref && o.lane != j.lane {
			return 0, fmt.Errorf("%s is already queued for %s — wait for it, or use another tag", j.ref, o.lane)
		}
	}
	q.seq++
	j.id = fmt.Sprintf("%s-%d", q.idPrefix, q.seq)
	j.status, j.created = jobQueued, time.Now()
	q.jobs = append(q.jobs, j)
	position, _ = q.wait(j, j.created)
	if !q.workers[j.lane] {
		q.workers[j.lane] = true
		go q.work(j.lane)
	}
	return position, nil
}

// work runs lane's jobs one at a time until none are queued.
func (q *buildQueue) work(lane string) {
	for {
		j, ctx := q.next(lane)
		if j == nil {
			return
		}
		ctx = context.WithValue(ctx, buildEngineKey{}, func(e string) { q.setBuildEngine(j, e) })
		err := j.run(ctx, func(line string) { q.appendLine(j, line) })
		q.finish(j, ctx, err)
	}
}

// buildEngineKey carries, in a running job's context, the callback that
// records which engine compiles it (the builder decides at run time).
type buildEngineKey struct{}

// reportBuildEngine returns the callback a job's run function hands the
// builder (BuildOptions.OnPlan); a no-op outside a queue worker.
func reportBuildEngine(ctx context.Context) func(string) {
	if f, ok := ctx.Value(buildEngineKey{}).(func(string)); ok {
		return f
	}
	return func(string) {}
}

func (q *buildQueue) setBuildEngine(j *buildJob, engine string) {
	q.mu.Lock()
	j.buildEng = engine
	q.mu.Unlock()
}

// next marks lane's oldest queued job running and returns it with its context,
// or retires the lane's worker when nothing is queued (under the same lock
// enqueue checks, so a job can't slip in unseen).
func (q *buildQueue) next(lane string) (*buildJob, context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.lane == lane && j.status == jobQueued {
			ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
			j.status, j.started, j.cancel = jobRunning, time.Now(), cancel
			return j, ctx
		}
	}
	delete(q.workers, lane)
	return nil, nil
}

// finish records a run's outcome. A requested cancel wins over the error it
// causes (a killed engine CLI reads like an OOM kill to ClassifyEngineError).
func (q *buildQueue) finish(j *buildJob, ctx context.Context, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	j.cancel()
	j.finished = time.Now()
	switch {
	case err == nil:
		j.status = jobDone
		q.addLine(j, "DONE")
	case j.canceled:
		j.status = jobCanceled
		q.addLine(j, "CANCELED")
	case timedOut:
		j.status = jobError
		j.errMsg = fmt.Sprintf("Timed out after %s.", q.timeout)
		q.addLine(j, "ERROR: "+j.errMsg)
	default:
		j.status = jobError
		// Lead with the recognized cause (engine down / disk / memory / port);
		// the full engine output was already streamed into the log.
		msg := err.Error()
		if hint := builder.ClassifyEngineError(err); hint != "" && hint != msg {
			q.addError(j, hint)
			q.addLine(j, "detail: "+msg)
		} else {
			q.addError(j, msg)
		}
	}
	q.prune()
}

// addError logs a (possibly multi-line) error as "ERROR: <first line>" plus the
// rest, and keeps the first line for the panel.
func (q *buildQueue) addError(j *buildJob, msg string) {
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	j.errMsg = lines[0]
	q.addLine(j, "ERROR: "+lines[0])
	for _, l := range lines[1:] {
		q.addLine(j, l)
	}
}

func (q *buildQueue) appendLine(j *buildJob, line string) {
	q.mu.Lock()
	q.addLine(j, line)
	q.mu.Unlock()
}

// addLine appends to j's log, trimming the oldest tenth when it outgrows
// maxJobLines. Callers hold q.mu.
func (q *buildQueue) addLine(j *buildJob, line string) {
	j.lines = append(j.lines, line)
	if len(j.lines) > maxJobLines {
		drop := len(j.lines) - maxJobLines + maxJobLines/10
		j.lines = append([]string(nil), j.lines[drop:]...)
		j.trimmed += drop
	}
}

// cancelJob cancels a queued job outright, or stops a running one by canceling
// its context (which kills the engine CLI; the worker then marks it canceled).
// It returns the job's status after the request.
func (q *buildQueue) cancelJob(id string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.find(id)
	switch {
	case j == nil:
		return "", errJobNotFound
	case j.status == jobQueued:
		j.status, j.finished = jobCanceled, time.Now()
		q.addLine(j, "CANCELED before it started.")
		q.prune()
		return jobCanceled, nil
	case j.status == jobRunning:
		if !j.canceled {
			j.canceled = true
			q.addLine(j, "Canceling …")
			j.cancel()
		}
		return jobRunning, nil
	default:
		return j.status, errJobFinished
	}
}

// prune drops the oldest finished jobs beyond keepFinishedJobs. Callers hold q.mu.
func (q *buildQueue) prune() {
	finished := 0
	for _, j := range q.jobs {
		if !j.active() {
			finished++
		}
	}
	drop := finished - keepFinishedJobs
	if drop <= 0 {
		return
	}
	kept := make([]*buildJob, 0, len(q.jobs)-drop)
	for _, j := range q.jobs {
		if drop > 0 && !j.active() {
			drop--
			continue
		}
		kept = append(kept, j)
	}
	q.jobs = kept
}

func (q *buildQueue) find(id string) *buildJob {
	for _, j := range q.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

// wait is how many unfinished jobs are ahead of j in its lane, and an estimate
// of the seconds until they're done from their ETAs (0 when one is unknown).
// Callers hold q.mu.
func (q *buildQueue) wait(j *buildJob, now time.Time) (ahead, secs int) {
	known := true
	for _, o := range q.jobs {
		if o == j {
			break
		}
		if o.lane != j.lane || !o.active() {
			continue
		}
		ahead++
		if o.eta <= 0 {
			known = false
			continue
		}
		rem := o.eta
		if o.status == jobRunning {
			if rem -= int(now.Sub(o.started).Seconds()); rem < 0 {
				rem = 0
			}
		}
		secs += rem
	}
	if !known {
		secs = 0
	}
	return ahead, secs
}

// jobSummary is a job's row in GET /api/builds (and the head of its state).
type jobSummary struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"`   // model | ensemble
	Status     string      `json:"status"` // queued | running | done | error | canceled
	Active     bool        `json:"active"` // queued or running
	Ref        string      `json:"ref"`
	ModelID    string      `json:"model_id"`
	Name       string      `json:"name"`
	Engine     string      `json:"engine"`
	BuildEng   string      `json:"build_engine,omitempty"` // engine that compiles it, once chosen; differs from engine when the image is transferred
	Compute    string      `json:"compute"`
	Config     buildConfig `json:"config"`
	Position   int         `json:"position"`     // queued: unfinished jobs ahead in its engine's lane
	WaitSecs   int         `json:"wait_seconds"` // queued: estimated seconds until it starts (0 = unknown)
	CreatedAt  int64       `json:"created_at"`   // unix ms
	StartedAt  int64       `json:"started_at"`   // unix ms, 0 = not yet
	FinishedAt int64       `json:"finished_at"`  // unix ms, 0 = not yet
	ElapsedMs  int64       `json:"elapsed_ms"`   // run time so far (or in total)
	EtaSeconds int         `json:"eta_seconds"`  // expected run time, 0 = unknown
	LastLine   string      `json:"last_line"`
	Error      string      `json:"error,omitempty"`
	LineCount  int         `json:"line_count"` // total log lines = the next offset
}

// buildSnapshot is one job's state plus its log from an offset — the shape of
// GET /api/builds/state and the legacy GET /api/build/state.
type buildSnapshot struct {
	jobSummary
	Lines      []string `json:"lines"`
	NextOffset int      `json:"next_offset"`
}

func unixMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// summary describes j at now. Callers hold q.mu.
func (q *buildQueue) summary(j *buildJob, now time.Time) jobSummary {
	s := jobSummary{
		ID: j.id, Kind: j.kind, Status: j.status, Active: j.active(), Ref: j.ref,
		ModelID: j.cfg.ModelID, Name: j.name, Engine: j.lane, BuildEng: j.buildEng, Compute: j.cfg.Compute, Config: j.cfg,
		CreatedAt: unixMs(j.created), StartedAt: unixMs(j.started), FinishedAt: unixMs(j.finished),
		EtaSeconds: j.eta, Error: j.errMsg, LineCount: j.trimmed + len(j.lines),
	}
	if !j.started.IsZero() {
		end := now
		if !j.finished.IsZero() {
			end = j.finished
		}
		s.ElapsedMs = end.Sub(j.started).Milliseconds()
	}
	if n := len(j.lines); n > 0 {
		s.LastLine = j.lines[n-1]
	}
	if j.status == jobQueued {
		s.Position, s.WaitSecs = q.wait(j, now)
	}
	return s
}

// list summarizes every job, in submission order.
func (q *buildQueue) list() []jobSummary {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	out := make([]jobSummary, 0, len(q.jobs))
	for _, j := range q.jobs {
		out = append(out, q.summary(j, now))
	}
	return out
}

// snapshot is job id's state with its log from offset on; ok=false if unknown.
func (q *buildQueue) snapshot(id string, offset int) (buildSnapshot, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.find(id)
	if j == nil {
		return buildSnapshot{}, false
	}
	total := j.trimmed + len(j.lines)
	// A reader behind the trimmed head resumes at the oldest line still kept.
	offset = max(offset, j.trimmed)
	offset = min(offset, total)
	return buildSnapshot{
		jobSummary: q.summary(j, time.Now()),
		Lines:      append([]string{}, j.lines[offset-j.trimmed:]...),
		NextOffset: total,
	}, true
}

// current is the job the legacy single-build endpoint reports: the running job
// that started first, else the most recent one ("" when there are none).
func (q *buildQueue) current() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var cur *buildJob
	for _, j := range q.jobs {
		if j.status == jobRunning && (cur == nil || j.started.Before(cur.started)) {
			cur = j
		}
	}
	if cur == nil && len(q.jobs) > 0 {
		cur = q.jobs[len(q.jobs)-1]
	}
	if cur == nil {
		return ""
	}
	return cur.id
}

// ---- handlers ---------------------------------------------------------------

// handleBuilds lists the queue: GET /api/builds -> {"jobs": [jobSummary...]}.
func (s *Server) handleBuilds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.q.list()})
}

// handleBuildsState streams one job's log: GET /api/builds/state?id=&offset=.
// Without an id it reports the legacy current job.
func (s *Server) handleBuildsState(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		s.handleBuildState(w, r)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	snap, ok := s.q.snapshot(id, offset)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errJobNotFound.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// handleBuildState is the legacy single-build view, GET /api/build/state?offset=:
// the running job, else the most recent one (an idle factory returns an empty,
// inactive snapshot, as before the queue).
func (s *Server) handleBuildState(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if snap, ok := s.q.snapshot(s.q.current(), offset); ok {
		writeJSON(w, http.StatusOK, snap)
		return
	}
	writeJSON(w, http.StatusOK, buildSnapshot{Lines: []string{}})
}

// handleBuildsCancel cancels a job: POST /api/builds/cancel {"id": "..."} ->
// {"id", "status"} (a running job reports "running" until its worker stops it).
func (s *Server) handleBuildsCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status, err := s.q.cancelJob(req.ID)
	switch {
	case errors.Is(err, errJobNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "status": status})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"id": req.ID, "status": status})
	}
}

// enqueueResponse answers POST /api/build and /api/ensemble/build: the job id and
// how many jobs are ahead of it on its engine. "status" stays for older UIs.
func enqueueResponse(w http.ResponseWriter, j *buildJob, position int) {
	status := "queued"
	if position == 0 {
		status = "started"
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "id": j.id, "position": position, "engine": j.lane})
}

// ---- reorder and retry --------------------------------------------------------

var (
	errJobNotQueued = errors.New("only a build that hasn't started can be moved")
	errJobActive    = errors.New("that build is still queued or running")
	errBadMove      = errors.New(`"to" must be up, down, top or bottom`)
)

// move reorders a queued job among the queued jobs of its own lane (to: up,
// down, top, bottom). Jobs run in slice order per lane, so this swaps slice
// positions; other lanes' jobs keep their places.
func (q *buildQueue) move(id, to string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.find(id)
	if j == nil {
		return errJobNotFound
	}
	if j.status != jobQueued {
		return errJobNotQueued
	}
	// Indexes (in q.jobs) of this lane's queued jobs, in run order.
	var lane []int
	at := -1
	for i, o := range q.jobs {
		if o.lane == j.lane && o.status == jobQueued {
			if o == j {
				at = len(lane)
			}
			lane = append(lane, i)
		}
	}
	dest := at
	switch to {
	case "up":
		dest = at - 1
	case "down":
		dest = at + 1
	case "top":
		dest = 0
	case "bottom":
		dest = len(lane) - 1
	default:
		return errBadMove
	}
	if dest < 0 || dest >= len(lane) || dest == at {
		return nil // already there
	}
	// Rotate the lane's queued jobs within their own slots.
	order := make([]*buildJob, len(lane))
	for k, i := range lane {
		order[k] = q.jobs[i]
	}
	moved := order[at]
	order = append(order[:at], order[at+1:]...)
	order = append(order[:dest], append([]*buildJob{moved}, order[dest:]...)...)
	for k, i := range lane {
		q.jobs[i] = order[k]
	}
	return nil
}

// retry queues a finished job again with the same settings, as a new job.
func (q *buildQueue) retry(id string) (*buildJob, int, error) {
	q.mu.Lock()
	j := q.find(id)
	if j == nil {
		q.mu.Unlock()
		return nil, 0, errJobNotFound
	}
	if j.active() {
		q.mu.Unlock()
		return nil, 0, errJobActive
	}
	for _, o := range q.jobs {
		if o.active() && o.ref == j.ref && o.lane == j.lane {
			q.mu.Unlock()
			return nil, 0, fmt.Errorf("%s is already queued for %s", j.ref, j.lane)
		}
	}
	nj := &buildJob{kind: j.kind, lane: j.lane, ref: j.ref, name: j.name, cfg: j.cfg, eta: j.eta, run: j.run}
	q.mu.Unlock()
	pos, err := q.enqueue(nj)
	return nj, pos, err
}

// handleBuildsMove reorders a queued build: POST /api/builds/move {"id", "to"}.
func (s *Server) handleBuildsMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	switch err := s.q.move(req.ID, req.To); {
	case errors.Is(err, errJobNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, errBadMove):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"jobs": s.q.list()})
	}
}

// handleBuildsRetry queues a finished build again: POST /api/builds/retry {"id"}
// -> the same answer as POST /api/build.
func (s *Server) handleBuildsRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	j, pos, err := s.q.retry(req.ID)
	switch {
	case errors.Is(err, errJobNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		enqueueResponse(w, j, pos)
	}
}

// running reports whether any build is running right now.
func (q *buildQueue) running() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.status == jobRunning {
			return true
		}
	}
	return false
}

// activeByLane counts queued + running jobs per engine.
func (q *buildQueue) activeByLane() map[string]int {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := map[string]int{}
	for _, j := range q.jobs {
		if j.active() {
			out[j.lane]++
		}
	}
	return out
}

// whileIdle runs fn only when no build is running, holding the queue lock so
// none can start meanwhile (a starting job takes the lock in next). It reports
// whether fn ran.
func (q *buildQueue) whileIdle(fn func()) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.status == jobRunning {
			return false
		}
	}
	fn()
	return true
}
