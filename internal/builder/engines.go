package builder

// Engine machines. Docker Desktop and the Podman machine are separate VMs with
// their own memory, CPUs and GPU access — on a Mac the Podman (libkrun) machine
// is the GPU path but can be far smaller than Docker's. This file reads each
// engine's resources, decides where a from-source build compiles (a compile on
// a too-small VM gets OOM-killed half an hour in), moves an image built on one
// engine to the one it runs on, and turns GPU/memory failures into actionable
// hints.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/catalog"
)

// Least memory (GB) an engine's machine needs to compile a runtime from source.
// The GPU backends are the heavy ones: one compiler job on ggml-vulkan.cpp or
// the CUDA kernels takes up to ~2–3 GB, and the VM's kernel and BuildKit need
// about 1 GB besides, so below ~6 GB even a one-job build risks the OOM killer.
// CPU-only ggml compiles in ~1.5 GB per job; 3 GB leaves room for one job plus
// the VM. (The Dockerfiles size their parallel jobs to the memory left over —
// see BUILD_JOBS there.) Observed: a Vulkan build on a 2 GiB, 7-CPU Podman
// machine was OOM-killed in cc1plus after 29 minutes.
const (
	cpuCompileFloorGB = 3.0
	gpuCompileFloorGB = 6.0
)

// compileFloorGB is the engine memory a from-source build for compute needs.
func compileFloorGB(compute string) float64 {
	if compute == "cuda" || compute == "vulkan" {
		return gpuCompileFloorGB
	}
	return cpuCompileFloorGB
}

// CompileFloors maps each compute to compileFloorGB, for the UI's hints.
func CompileFloors() map[string]float64 {
	return map[string]float64{"cpu": cpuCompileFloorGB, "cuda": gpuCompileFloorGB, "vulkan": gpuCompileFloorGB}
}

// compilesFromSource reports whether a Dockerfile compiles C/C++ (a cmake build
// step). Images that only install packages or reuse a prebuilt runtime (Piper,
// Kokoro, audio.cpp) build fine on a small machine and skip the memory preflight.
func compilesFromSource(dockerfile []byte) bool {
	return strings.Contains(string(dockerfile), "cmake --build")
}

// Compiles reports whether building m compiles a runtime from source — true
// when any of its runtime family's Dockerfiles does. The UI uses it to say where
// a GPU build will compile before it starts.
func (b *Builder) Compiles(m catalog.Model) bool {
	family := modelFamily(m)
	for _, c := range []string{"cpu", "cuda", "vulkan"} {
		data, err := os.ReadFile(filepath.Join(b.BaseDir, "docker", dockerfileName(family, c)))
		if err == nil && compilesFromSource(data) {
			return true
		}
	}
	return false
}

// otherEngine is the engine that isn't e.
func otherEngine(e string) string {
	if resolveEngine(e) == enginePodman {
		return engineDocker
	}
	return enginePodman
}

// engineLabel names an engine's machine in user-facing messages.
func engineLabel(e string) string {
	if resolveEngine(e) == enginePodman {
		return "the Podman machine"
	}
	return "Docker"
}

// MemoryFix is how to give an engine's machine more memory: the Podman machine
// is resized from the host CLI (krunkit keeps the factory's mounted socket only
// until the machine restarts, so the factory needs a restart after it), Docker
// Desktop from its settings.
func MemoryFix(engine string) string {
	if resolveEngine(engine) == enginePodman {
		return "podman machine stop && podman machine set --memory 12288 && podman machine start"
	}
	return "Docker Desktop → Settings → Resources → Memory"
}

// memoryFixSentence is MemoryFix as a complete instruction for error text.
func memoryFixSentence(engine string) string {
	if resolveEngine(engine) == enginePodman {
		return "give the Podman machine more memory, then restart the factory (scripts/<your-os>/start.sh) so it reconnects:\n  " + MemoryFix(engine)
	}
	return "raise Docker Desktop's memory: " + MemoryFix(engine) + "."
}

// engineInfoFormat is the `info` template for an engine machine's total memory
// (bytes) and CPU count. Podman reports them under .Host, and calls the CPU
// count CPUs (docker's is the top-level NCPU) — asking podman for .Host.NCPU
// fails the template, which used to drop podman and report Docker's numbers.
func engineInfoFormat(engine string) string {
	if resolveEngine(engine) == enginePodman {
		return "{{.Host.MemTotal}} {{.Host.CPUs}}"
	}
	return "{{.MemTotal}} {{.NCPU}}"
}

// parseMemCPU parses engineInfoFormat's output: "<bytes> <cpus>".
func parseMemCPU(out string) (memBytes int64, cpus int, ok bool) {
	f := strings.Fields(out)
	if len(f) < 2 {
		return 0, 0, false
	}
	m, err1 := strconv.ParseInt(f[0], 10, 64)
	c, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || m <= 0 {
		return 0, 0, false
	}
	return m, c, true
}

// EngineMemory reads an engine machine's total memory (GB, 1e9 bytes) and CPU
// count through its remote `info` API — which works from inside the
// containerized factory, unlike the host-only `podman machine inspect`.
func EngineMemory(ctx context.Context, engine string) (memGB float64, cpus int, err error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := engineCmd(ctx, engine, "info", "--format", engineInfoFormat(engine)).Output()
	if err != nil {
		return 0, 0, err
	}
	memBytes, cpus, ok := parseMemCPU(string(out))
	if !ok {
		return 0, 0, fmt.Errorf("%s info: unexpected output %q", resolveEngine(engine), strings.TrimSpace(string(out)))
	}
	return float64(memBytes) / 1e9, cpus, nil
}

// ----------------------------------------------------------------------------
// Per-engine resources (/api/sysinfo)
// ----------------------------------------------------------------------------

// EngineResources is one engine's machine as its containers see it.
type EngineResources struct {
	Engine    string  `json:"engine"`
	Reachable bool    `json:"reachable"` // answered `info` just now
	MemGB     float64 `json:"mem_gb"`
	CPUs      int     `json:"cpus"`
	GPU       string  `json:"gpu"` // compute it gives containers: "" | "vulkan" | "cuda"
	// LowMemory flags a GPU engine with less memory than compiling a GPU image
	// needs: its GPU builds compile on the other engine and are transferred, and
	// only models that fit in MemGB can run on its GPU.
	LowMemory bool   `json:"low_memory,omitempty"`
	Fix       string `json:"fix"` // how to give this machine more memory
}

// SysInfo reports the engines' machines so the UI can show "fits your system"
// guidance and pick sensible build defaults. The top-level fields are kept for
// older UIs: they describe the GPU engine when it's reachable (where GPU models
// run), else the first engine that answers. Best-effort: zero/empty fields when
// undeterminable.
type SysInfo struct {
	MemGB  float64 `json:"mem_gb"`
	CPUs   int     `json:"cpus"`
	GPU    string  `json:"gpu"`     // "" | "vulkan" | "cuda" (of the engine above)
	VRAMGB float64 `json:"vram_gb"` // total GPU VRAM (GB), 0 if unknown
	Engine string  `json:"engine"`  // engine the numbers came from

	Engines []EngineResources `json:"engines"`
	// Build-form defaults for this machine: a Mac with a libkrun Podman machine
	// gets podman + vulkan, an NVIDIA host docker + cuda, anything else docker + cpu.
	DefaultEngine  string `json:"default_engine"`
	DefaultCompute string `json:"default_compute"`
	// CompileMinGB is the least engine memory a from-source build needs, per compute.
	CompileMinGB map[string]float64 `json:"compile_min_gb"`
	// HostMemGB is the computer's own memory (GiB), which both engine VMs
	// share; 0 when the factory can't tell (see HostMemGB).
	HostMemGB float64 `json:"host_mem_gb,omitempty"`
}

// engineGPU maps FACTORY_GPU onto the engine that provides it: start.sh sets
// "vulkan" when it finds a libkrun Podman machine (the Mac GPU path, a Podman-only
// device), start.ps1 sets "cuda" on NVIDIA hosts (Docker + the NVIDIA Container
// Toolkit).
func engineGPU(factoryGPU, engine string) string {
	switch {
	case factoryGPU == "vulkan" && engine == enginePodman:
		return "vulkan"
	case factoryGPU == "cuda" && engine == engineDocker:
		return "cuda"
	}
	return ""
}

// SysInfo queries every installed engine (concurrently) and summarizes them.
// GPU comes from FACTORY_GPU (set by the start scripts, which can see the host),
// VRAM from FACTORY_VRAM.
func (b *Builder) SysInfo(ctx context.Context) SysInfo {
	factoryGPU := os.Getenv("FACTORY_GPU")
	si := SysInfo{CompileMinGB: CompileFloors(), HostMemGB: HostMemGB(ctx)}
	if v, err := strconv.ParseFloat(os.Getenv("FACTORY_VRAM"), 64); err == nil {
		si.VRAMGB = v
	}
	engines := candidateEngines()
	si.Engines = make([]EngineResources, len(engines))
	var wg sync.WaitGroup
	for i, e := range engines {
		wg.Add(1)
		go func(i int, e string) {
			defer wg.Done()
			r := EngineResources{Engine: e, GPU: engineGPU(factoryGPU, e), Fix: MemoryFix(e)}
			if mem, cpus, err := EngineMemory(ctx, e); err == nil {
				r.Reachable, r.MemGB, r.CPUs = true, mem, cpus
			}
			si.Engines[i] = r
		}(i, e)
	}
	wg.Wait()
	si.summarize(factoryGPU)
	return si
}

// summarize flags low-memory GPU engines, fills the legacy top-level fields, and
// picks the build form's defaults.
func (si *SysInfo) summarize(factoryGPU string) {
	var top *EngineResources
	for i := range si.Engines {
		e := &si.Engines[i]
		e.LowMemory = e.Reachable && e.GPU != "" && e.MemGB < gpuCompileFloorGB
		if e.Reachable && (top == nil || (e.GPU != "" && top.GPU == "")) {
			top = e
		}
	}
	if top != nil {
		si.MemGB, si.CPUs, si.GPU, si.Engine = top.MemGB, top.CPUs, top.GPU, top.Engine
	}
	si.DefaultEngine, si.DefaultCompute = defaultTarget(factoryGPU, si.Engines)
}

// defaultTarget is the build form's starting engine + compute: the engine that
// provides FACTORY_GPU when it's installed (podman + vulkan on a Mac with a
// libkrun machine, docker + cuda on an NVIDIA host), else CPU on the first engine
// that answers (docker first) — CPU images run anywhere.
func defaultTarget(factoryGPU string, engines []EngineResources) (engine, compute string) {
	for _, e := range engines {
		if e.GPU != "" && e.GPU == factoryGPU {
			return e.Engine, e.GPU
		}
	}
	for _, e := range engines {
		if e.Reachable {
			return e.Engine, "cpu"
		}
	}
	return engineDocker, "cpu"
}

// ----------------------------------------------------------------------------
// Build preflight: where does a from-source build compile?
// ----------------------------------------------------------------------------

// buildPlan says where an image compiles and how it reaches its run engine.
type buildPlan struct {
	Build    string  // engine that runs `<engine> build`
	Target   string  // engine the image runs on (its local-llm.engine label)
	Transfer bool    // Build != Target: `<Build> save` + `<Target> load` afterwards
	BuildGB  float64 // memory of the compiling engine's machine (0 = unknown)
	Note     string  // why, for the build log ("" when there's nothing to say)
}

// engineMem is one engine's answer to the preflight memory query.
type engineMem struct {
	GB  float64 // total memory; 0 when unknown
	Err error   // the query failed (unreachable, not installed, …)
}

func (m engineMem) known() bool { return m.Err == nil && m.GB > 0 }

// decideBuild picks where a from-source build of compute compiles, given each
// engine's memory. It builds on the target when that has needGB (or its memory
// can't be read); borrows the other engine when only that one has enough — the
// image is then transferred to the target, and its local-llm.engine label still
// names the target, which is what Run reads; and otherwise fails before any
// download or compile with the exact fix, instead of being OOM-killed half an
// hour in. An unreachable target fails right away too.
func decideBuild(target, compute string, needGB float64, mem map[string]engineMem) (buildPlan, error) {
	plan := buildPlan{Build: target, Target: target}
	t := mem[target]
	switch {
	case t.Err != nil && transientEngineErr(t.Err):
		return plan, fmt.Errorf("%s build: %w", target, t.Err)
	case !t.known():
		plan.Note = fmt.Sprintf("Couldn't read %s's memory — building there anyway.", engineLabel(target))
		return plan, nil
	case t.GB >= needGB:
		plan.BuildGB = t.GB
		return plan, nil
	}
	other := otherEngine(target)
	if o := mem[other]; o.known() && o.GB >= needGB {
		plan.Build, plan.Transfer, plan.BuildGB = other, true, o.GB
		plan.Note = fmt.Sprintf("%s has %.1f GB — too little to compile this %s image (it needs at least %.0f GB) — so it compiles on %s (%.1f GB) and is then transferred to %s.",
			capitalize(engineLabel(target)), t.GB, compute, needGB, engineLabel(other), o.GB, engineLabel(target))
		return plan, nil
	}
	return plan, notEnoughMemory(target, compute, needGB, mem)
}

// notEnoughMemory is the build error when no engine can compile the image.
func notEnoughMemory(target, compute string, needGB float64, mem map[string]engineMem) error {
	other := otherEngine(target)
	has := func(e string) string {
		if m := mem[e]; m.known() {
			return fmt.Sprintf("%s has %.1f GB", engineLabel(e), m.GB)
		}
		return engineLabel(e) + " isn't available"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Not enough memory to compile this %s image: building it from source needs at least %.0f GB on the engine that compiles it, but %s and %s.",
		compute, needGB, has(target), has(other))
	fmt.Fprintf(&sb, "\nTo fix it, %s", memoryFixSentence(target))
	if _, installed := lookEngine(other); installed || mem[other].known() {
		fmt.Fprintf(&sb, "\nOr %s", memoryFixSentence(other))
	}
	return errors.New(sb.String())
}

// planBuild runs the memory preflight for a from-source build: it reads the
// target engine's memory, and the other engine's only when the target is too
// small, then lets decideBuild choose.
func (b *Builder) planBuild(ctx context.Context, target, compute string, log LogFunc) (buildPlan, error) {
	need := compileFloorGB(compute)
	mem := map[string]engineMem{}
	query := func(e string) {
		gb, _, err := EngineMemory(ctx, e)
		mem[e] = engineMem{GB: gb, Err: err}
	}
	query(target)
	if t := mem[target]; t.known() && t.GB < need {
		if _, ok := lookEngine(otherEngine(target)); ok {
			query(otherEngine(target))
		}
	}
	plan, err := decideBuild(target, compute, need, mem)
	if err != nil {
		return plan, err
	}
	if t := mem[target]; t.known() {
		log(fmt.Sprintf("Memory check: %s has %.1f GB; compiling a %s image needs at least %.0f GB.", engineLabel(target), t.GB, compute, need))
	}
	if plan.Note != "" {
		log(plan.Note)
	}
	return plan, nil
}

// capitalize upper-cases the first letter ("the Podman machine" -> "The Podman machine").
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ----------------------------------------------------------------------------
// One compile per engine machine at a time
// ----------------------------------------------------------------------------

// acquireBuildSlot serializes `<engine> build` per engine machine. The build
// queue already runs each engine's jobs one at a time, but a job can borrow the
// other engine to compile (the low-memory fallback), and two compiles on one
// machine would each size their parallel jobs to its whole memory.
func (b *Builder) acquireBuildSlot(ctx context.Context, engine string, log LogFunc) (release func(), err error) {
	b.slotMu.Lock()
	if b.slots == nil {
		b.slots = map[string]chan struct{}{}
	}
	ch := b.slots[engine]
	if ch == nil {
		ch = make(chan struct{}, 1)
		b.slots[engine] = ch
	}
	b.slotMu.Unlock()

	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	default:
	}
	log(fmt.Sprintf("Waiting for another build on %s to finish …", engineLabel(engine)))
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ----------------------------------------------------------------------------
// Moving an image between engines
// ----------------------------------------------------------------------------

// localName is how an engine names a locally built or loaded ref: podman
// qualifies a short name with "localhost/" (that's what `podman build -t` stores),
// docker keeps it as written.
func localName(engine, ref string) string {
	if resolveEngine(engine) != enginePodman || hasRegistry(ref) {
		return ref
	}
	return "localhost/" + ref
}

// hasRegistry reports whether ref's first path component is a registry host
// ("quay.io/x", "localhost/x", "host:5000/x") rather than a namespace.
func hasRegistry(ref string) bool {
	i := strings.IndexByte(ref, '/')
	if i < 0 {
		return false
	}
	first := ref[:i]
	return first == "localhost" || strings.ContainsAny(first, ".:")
}

// loadedName picks the image name a `load` reported for ref: "Loaded image:
// docker.io/local-llm/x:latest" (podman normalizes a docker archive's short
// names to docker.io), "Loaded image: localhost/x:latest" (docker loading a
// podman archive), or older podman's "Loaded image(s): a,b". "" when none fits.
func loadedName(lines []string, ref string) string {
	var names []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "Loaded image") {
			continue
		}
		i := strings.Index(l, ":")
		if i < 0 {
			continue
		}
		for _, n := range strings.Split(l[i+1:], ",") {
			if n = strings.TrimSpace(n); n != "" && !strings.HasPrefix(n, "sha256:") {
				names = append(names, n)
			}
		}
	}
	for _, n := range names {
		if n == ref || strings.HasSuffix(n, "/"+ref) {
			return n
		}
	}
	if len(names) == 1 {
		return names[0]
	}
	return ""
}

// transferImage moves a freshly built image from the engine that compiled it to
// the one it runs on, through a tar: the export under images/ when there is one
// (tarPath), else a temp tar under models/.build. `<to> load` streams the file
// from this filesystem — the podman client is remote, so the tar never has to
// be visible inside the Podman machine. The build engine's copy is removed
// afterwards so each image lives where it runs; its compile layers stay in that
// engine's build cache for the next build.
func (b *Builder) transferImage(ctx context.Context, from, to, ref, tarPath string, log LogFunc) error {
	if tarPath == "" {
		tarPath = filepath.Join(b.BuildDir, fmt.Sprintf("xfer-%d.tar", time.Now().UnixNano()))
		defer os.Remove(tarPath)
		log(fmt.Sprintf("Transfer 1/3: saving %s from %s ...", ref, engineLabel(from)))
		if err := runStreaming(ctx, b.BaseDir, from, log, "save", "-o", tarPath, ref); err != nil {
			return fmt.Errorf("%s save failed: %w", from, err)
		}
	} else {
		log(fmt.Sprintf("Transfer 1/3: using the export images/%s", filepath.Base(tarPath)))
	}
	if fi, err := os.Stat(tarPath); err == nil {
		log(fmt.Sprintf("Transfer 2/3: loading %.2f GB into %s ...", float64(fi.Size())/1e9, engineLabel(to)))
	}
	tail, err := runStreamingTail(ctx, b.BaseDir, to, log, 50, "load", "-i", tarPath)
	if err != nil {
		return fmt.Errorf("%s load failed: %w", to, err)
	}
	name := b.nameLoaded(ctx, to, ref, loadedName(tail, ref), log)
	if engineCmd(ctx, to, "image", "inspect", name).Run() != nil {
		return fmt.Errorf("%s load finished, but %s isn't in %s", to, name, engineLabel(to))
	}
	log(fmt.Sprintf("Loaded into %s as %s.", engineLabel(to), name))
	if out, err := engineCmd(ctx, from, "rmi", ref).CombinedOutput(); err != nil {
		log(fmt.Sprintf("Transfer 3/3: couldn't remove the %s copy (%s) — delete it from Images if you like.", engineLabel(from), strings.TrimSpace(string(out))))
	} else {
		log(fmt.Sprintf("Transfer 3/3: removed the %s copy (its compile cache stays for the next build).", engineLabel(from)))
	}
	return nil
}

// nameLoaded renames a loaded image to the name a native build on that engine
// would have given it (see localName), so a later native rebuild replaces it
// and the short ref resolves there. Returns the name the image ends up with.
func (b *Builder) nameLoaded(ctx context.Context, engine, ref, loaded string, log LogFunc) string {
	want := localName(engine, ref)
	if loaded == "" || loaded == want {
		return want
	}
	if out, err := engineCmd(ctx, engine, "tag", loaded, want).CombinedOutput(); err != nil {
		log(fmt.Sprintf("Couldn't name it %s (%s); it stays %s.", want, strings.TrimSpace(string(out)), loaded))
		return loaded
	}
	// Drop the loaded name so the image lists once. Podman removes a name with
	// untag; docker's rmi of one of several tags only untags.
	untag := []string{"untag", want, loaded}
	if resolveEngine(engine) == engineDocker {
		untag = []string{"rmi", loaded}
	}
	_ = engineCmd(ctx, engine, untag...).Run()
	return want
}

// runStreamingTail is runStreaming that also returns the last n output lines,
// so the caller can read a result line (`load`) or diagnose a failure.
func runStreamingTail(ctx context.Context, dir, engine string, log LogFunc, n int, args ...string) ([]string, error) {
	var tail []string
	// Stdout and Stderr share one writer, so exec calls it from one goroutine at
	// a time — no lock needed around tail.
	err := runStreaming(ctx, dir, engine, func(line string) {
		log(line)
		tail = append(tail, line)
		if len(tail) > n {
			tail = tail[len(tail)-n:]
		}
	}, args...)
	return tail, err
}

// ----------------------------------------------------------------------------
// Failure hints
// ----------------------------------------------------------------------------

// HintError is an engine failure whose cause was recognized where it happened:
// Hint is the short, actionable message ClassifyEngineError returns for it, and
// Err stays the raw detail.
type HintError struct {
	Hint string
	Err  error
}

func (e *HintError) Error() string { return e.Err.Error() }
func (e *HintError) Unwrap() error { return e.Err }

// compilerOOM reports whether build output shows the compiler OOM-killed: gcc
// prints "Killed signal terminated program cc1plus" when the kernel kills it.
func compilerOOM(lines []string) bool {
	for _, l := range lines {
		l = strings.ToLower(l)
		if strings.Contains(l, "killed signal terminated program") ||
			strings.Contains(l, "virtual memory exhausted") ||
			strings.Contains(l, "out of memory allocating") {
			return true
		}
	}
	return false
}

// oomHint explains a compile that ran out of memory on engine (memGB, 0 = unknown).
func oomHint(engine string, memGB float64) string {
	have := ""
	if memGB > 0 {
		have = fmt.Sprintf(" (%.1f GB)", memGB)
	}
	return fmt.Sprintf("The compiler ran out of memory on %s%s and was killed. To fix it, %s\nOr lower the parallel compile jobs: set ARG BUILD_JOBS=1 in the image's docker/Dockerfile.*.",
		engineLabel(engine), have, memoryFixSentence(engine))
}

// gpuDeviceHint explains a Vulkan run that failed because the engine has no GPU
// render node (/dev/dri) to give the container; "" for any other failure.
// macDocker says the docker engine is Docker Desktop's Linux VM on a Mac.
func gpuDeviceHint(engine, compute, output string, macDocker bool) string {
	if compute != "vulkan" {
		return ""
	}
	o := strings.ToLower(output)
	if !strings.Contains(o, "/dev/dri") ||
		!(strings.Contains(o, "no such file") || strings.Contains(o, "error gathering device") || strings.Contains(o, "not found")) {
		return ""
	}
	if resolveEngine(engine) == enginePodman {
		return "This Podman machine has no GPU device (/dev/dri). GPU containers need a libkrun (krunkit) machine: podman machine init --provider libkrun — see the README's \"Running on a Mac\"."
	}
	if macDocker {
		return "Docker Desktop on this Mac has no GPU device under Apple Virtualization. Run this image on Podman, or switch Docker Desktop to Docker VMM (Settings → General → Virtual Machine Manager) and try again."
	}
	return "Docker has no GPU device (/dev/dri) on this host to give the container. Install the GPU's Mesa/Vulkan driver so /dev/dri exists, or run the image on another engine."
}

// dockerDesktopMac reports whether the docker engine is Docker Desktop's Linux
// VM on a Mac.
func dockerDesktopMac(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := engineCmd(ctx, engineDocker, "info", "--format", "{{.OperatingSystem}}|{{.KernelVersion}}|{{json .Labels}}").Output()
	return err == nil && isDockerDesktopMac(string(out))
}

// isDockerDesktopMac reads dockerDesktopMac's info line: Docker Desktop runs a
// LinuxKit kernel on macOS and Linux (WSL2's on Windows), and labels its CLI
// socket with the user's home — /Users/… on a Mac, /home/… on Linux.
func isDockerDesktopMac(info string) bool {
	s := strings.ToLower(info)
	return strings.Contains(s, "docker desktop") && strings.Contains(s, "linuxkit") && !strings.Contains(s, "/home/")
}

// ----------------------------------------------------------------------------
// Startup cleanup
// ----------------------------------------------------------------------------

// CleanStaleBuildDirs removes build contexts (ctx-*, ens-*) and transfer tars
// (xfer-*.tar) that a factory stopped mid-build left in BuildDir — a build only
// removes its own via defer, which never runs when the process dies. Call it at
// startup, before any build can be running. Returns what it removed.
func (b *Builder) CleanStaleBuildDirs() []string {
	var removed []string
	for _, pat := range []string{"ctx-*", "ens-*", "xfer-*.tar"} {
		matches, _ := filepath.Glob(filepath.Join(b.BuildDir, pat))
		for _, m := range matches {
			if err := os.RemoveAll(m); err == nil {
				removed = append(removed, filepath.Base(m))
			}
		}
	}
	return removed
}
