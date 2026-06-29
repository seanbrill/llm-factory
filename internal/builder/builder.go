// Package builder drives the local build/run lifecycle: download a GGUF into
// ./models, build a model-baked image (CPU, CUDA, or Vulkan), export it as a
// .tar into ./images, and run/stop/list containers via a container-engine CLI.
//
// It shells out to the engine CLI (`docker` or `podman`) rather than using an
// SDK to keep the dependency surface at zero (stdlib only) and to mirror exactly
// what a user would type by hand. The engine is chosen per operation (the UI has
// an explicit Docker/Podman selector); "" defaults to docker so the original
// behaviour is unchanged. Podman is what unlocks GPU on macOS, where a
// libkrun/krunkit machine paravirtualizes the Metal GPU into a Vulkan container.
package builder

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/local-llm/internal/catalog"
	"github.com/yourorg/local-llm/internal/ensemble"
)

// Builder owns the project directories. New ensures they exist — this is the
// "on init, place copies of model files in a project directory" behaviour.
type Builder struct {
	BaseDir   string
	ModelsDir string
	ImagesDir string
	BuildDir  string
	ConfigDir string
	// HostDir is the project path as seen by the HOST docker daemon. It equals
	// BaseDir when the factory runs natively, but differs when the factory runs
	// in a container (set via HOST_DIR) — needed to bind-mount files into sibling
	// containers (the Caddy proxy) that run on the host daemon.
	HostDir string
}

// LogFunc receives one human-readable progress line at a time.
type LogFunc func(string)

// New resolves baseDir and creates models/, images/, config/, and the temp dir.
func New(baseDir string) (*Builder, error) {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}
	hostDir := os.Getenv("HOST_DIR") // set by start scripts when dockerized
	if hostDir == "" {
		hostDir = abs
	}
	b := &Builder{
		BaseDir:   abs,
		ModelsDir: filepath.Join(abs, "models"),
		ImagesDir: filepath.Join(abs, "images"),
		ConfigDir: filepath.Join(abs, "config"),
		// Keep the temp build context on the SAME filesystem as models/ so the
		// (multi-GB) model can be hardlinked into it instead of copied. When
		// models/ is a bind mount (dockerized factory), a sibling .build dir
		// would be on a different fs and force a slow copy.
		BuildDir: filepath.Join(abs, "models", ".build"),
		HostDir:  hostDir,
	}
	for _, d := range []string{b.ModelsDir, b.ImagesDir, b.ConfigDir, b.BuildDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// ----------------------------------------------------------------------------
// Container engine (docker | podman)
//
// Every shell-out goes through these helpers so the engine is a single, explicit
// knob. The UI picks it per build/run; "" resolves to docker, preserving the
// original behaviour exactly. Podman is required for the macOS GPU path (a
// libkrun machine), but is otherwise a drop-in: its CLI is docker-compatible for
// build/run/save/ps/images/inspect/rm.
// ----------------------------------------------------------------------------

const (
	engineDocker = "docker"
	enginePodman = "podman"
)

// resolveEngine normalizes an engine name, defaulting to docker.
func resolveEngine(engine string) string {
	if engine == enginePodman {
		return enginePodman
	}
	return engineDocker
}

// engineCmd builds an *exec.Cmd for the chosen engine.
func engineCmd(ctx context.Context, engine string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, resolveEngine(engine), args...)
}

// candidateEngines returns the installed engines (docker first), used when a
// listing should span both. Falls back to docker so callers always try something.
func candidateEngines() []string {
	var out []string
	for _, e := range []string{engineDocker, enginePodman} {
		if _, err := exec.LookPath(e); err == nil {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		out = []string{engineDocker}
	}
	return out
}

// proxyEngine is the engine used to run the managed Caddy proxy: the first
// available (docker preferred). The proxy reaches model containers via their
// published host ports, so it works even if models run on the other engine.
func proxyEngine() string { return candidateEngines()[0] }

// runtimeFamily maps a model modality to the inference runtime that serves it.
// Each family has its own Dockerfile(s) (Dockerfile.<family>.<compute>, except
// llama.cpp which keeps the historical Dockerfile.<compute>) and its own baked
// entrypoint. The llama.cpp family covers everything it can serve directly
// (chat/code/reasoning, vision via mmproj, embeddings); other modalities need a
// purpose-built runtime.
func runtimeFamily(modality string) string {
	switch modality {
	case "image":
		return "sd" // stable-diffusion.cpp (sd-server, /v1/images/generations)
	case "video":
		return "video" // stable-diffusion.cpp sd-cli (vid_gen) behind videogate
	case "audio-stt":
		return "whisper" // whisper.cpp (whisper-server, /inference)
	case "tts":
		return "tts" // CPU Piper/Kokoro
	default:
		return "llama" // text|code|reasoning|vision|embedding
	}
}

// selfContained reports whether a family bakes its own model in the Dockerfile,
// so there's no GGUF on disk to download/stage: tts (Piper) and python (e.g.
// Kokoro, which pulls its own weights at build time).
func selfContained(family string) bool { return family == "tts" || family == "python" }

// ModelPath is where a catalog model's weights live (or will live) on disk.
func (b *Builder) ModelPath(m catalog.Model) string {
	return filepath.Join(b.ModelsDir, m.File)
}

// EnsureModel downloads the GGUF into ./models if it isn't already there.
func (b *Builder) EnsureModel(ctx context.Context, m catalog.Model, log LogFunc) (string, error) {
	dest := b.ModelPath(m)
	if fi, err := os.Stat(dest); err == nil && fi.Size() > 0 {
		log(fmt.Sprintf("Model already present: %s (%.2f GB)", filepath.Base(dest), float64(fi.Size())/1e9))
		return dest, nil
	}
	if err := b.downloadTo(ctx, m.URL, dest, m.File, log); err != nil {
		return "", err
	}
	return dest, nil
}

// MMProjPath is the on-disk path of a vision model's projector, or "" if the
// model declares none (text/code/etc.).
func (b *Builder) MMProjPath(m catalog.Model) string {
	if m.MMProjFile == "" {
		return ""
	}
	return filepath.Join(b.ModelsDir, m.MMProjFile)
}

// EnsureMMProj downloads a vision model's projector (mmproj) GGUF if it declares
// one and it isn't already present. Returns "" for non-vision models.
func (b *Builder) EnsureMMProj(ctx context.Context, m catalog.Model, log LogFunc) (string, error) {
	if m.MMProjFile == "" {
		return "", nil
	}
	dest := b.MMProjPath(m)
	if fi, err := os.Stat(dest); err == nil && fi.Size() > 0 {
		log(fmt.Sprintf("Projector already present: %s (%.2f GB)", filepath.Base(dest), float64(fi.Size())/1e9))
		return dest, nil
	}
	if m.MMProjURL == "" {
		return "", fmt.Errorf("model %s sets mmproj_file but no mmproj_url", m.ID)
	}
	if err := b.downloadTo(ctx, m.MMProjURL, dest, m.MMProjFile, log); err != nil {
		return "", err
	}
	return dest, nil
}

// StagedWeight is a downloaded extra weight plus the runtime Role it fills.
type StagedWeight struct {
	Path string
	File string
	Role string
}

// EnsureExtraFiles downloads every ExtraFile a model declares (a video model's
// VAE, T5 encoder, high-noise diffusion model) into ./models, returning their
// on-disk paths + roles. No-op for models without extra files.
func (b *Builder) EnsureExtraFiles(ctx context.Context, m catalog.Model, log LogFunc) ([]StagedWeight, error) {
	out := make([]StagedWeight, 0, len(m.ExtraFiles))
	for _, w := range m.ExtraFiles {
		if w.File == "" || w.URL == "" {
			return nil, fmt.Errorf("model %s has an extra_file missing file or url", m.ID)
		}
		dest := filepath.Join(b.ModelsDir, w.File)
		if fi, err := os.Stat(dest); err != nil || fi.Size() == 0 {
			if err := b.downloadTo(ctx, w.URL, dest, w.File, log); err != nil {
				return nil, err
			}
		} else {
			log(fmt.Sprintf("Extra weight already present: %s (%.2f GB)", w.File, float64(fi.Size())/1e9))
		}
		out = append(out, StagedWeight{Path: dest, File: w.File, Role: w.Role})
	}
	return out, nil
}

// downloadTo streams url -> dest atomically (via a .part file) with progress
// logging. Shared by the model and projector downloaders.
func (b *Builder) downloadTo(ctx context.Context, url, dest, label string, log LogFunc) error {
	log(fmt.Sprintf("Downloading %s ...", label))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("download failed: HTTP %d (%s)", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, log: log, label: label}
	if _, err := io.Copy(f, pr); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download interrupted: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	log("Saved " + label + " to models/")
	return nil
}

// BuildOptions configures a single image build.
type BuildOptions struct {
	Model        catalog.Model
	ImageName    string // e.g. "myorg/qwen-analyzer"
	Tag          string // defaults to "latest"
	Engine       string // "docker" (default) | "podman"
	Compute      string // "cpu" | "cuda" | "vulkan"
	ExportTar    bool   // also `<engine> save` into ./images
	SystemPrompt string // optional baked-in initialization prompt
	InjectMode   string // "missing" (default) | "always"
	// Deploy defaults baked as labels so Run can apply them and the UI can clone:
	CtxSize   int     // context window (tokens); baked into CTX_SIZE
	MemoryGB  float64 // container memory limit at run time (0 = no limit)
	Route     string  // local URL alias -> http://<route>.localhost
	Autostart bool    // run with --restart unless-stopped
}

// Ref is the full image reference, e.g. "myorg/qwen-analyzer:latest".
func (o BuildOptions) Ref() string {
	tag := o.Tag
	if tag == "" {
		tag = "latest"
	}
	return o.ImageName + ":" + tag
}

// Build downloads the model (if needed), assembles a minimal build context, and
// runs `docker build` (+ optional `docker save`). Progress streams via log.
func (b *Builder) Build(ctx context.Context, o BuildOptions, log LogFunc) error {
	if strings.TrimSpace(o.ImageName) == "" {
		return fmt.Errorf("image name is required")
	}
	compute := o.Compute
	if compute == "" {
		compute = "cpu"
	}
	if compute != "cpu" && compute != "cuda" && compute != "vulkan" {
		return fmt.Errorf("unknown compute %q (want cpu, cuda, or vulkan)", compute)
	}
	engine := resolveEngine(o.Engine)

	// Select the runtime family. A model with runtime "python" routes to the
	// PyTorch family (Dockerfile.python.<compute> + pygate) regardless of
	// modality; otherwise the family is chosen by modality. llama.cpp keeps the
	// historical Dockerfile.<compute>; other families use Dockerfile.<family>.<compute>.
	family := runtimeFamily(o.Model.Mod())
	if o.Model.Rt() == "python" {
		family = "python"
	}
	dfName := "Dockerfile." + compute
	if family != "llama" {
		dfName = "Dockerfile." + family + "." + compute
	}
	dockerfileSrc := filepath.Join(b.BaseDir, "docker", dfName)
	dfData, err := os.ReadFile(dockerfileSrc)
	if err != nil {
		return fmt.Errorf("read %s (modality %q): %w", dockerfileSrc, o.Model.Mod(), err)
	}

	// Self-contained families (tts, python) bake their own model, so there's no
	// GGUF to download/stage. Every other family needs the model on disk.
	var modelPath string
	if !selfContained(family) {
		modelPath, err = b.EnsureModel(ctx, o.Model, log)
		if err != nil {
			return err
		}
	}

	// Assemble a tiny build context: just the Dockerfile + a hardlink to the
	// model (named model.gguf, which the Dockerfiles COPY). Hardlinking avoids
	// copying multi-GB weights and keeps the context off the other models.
	work := filepath.Join(b.BuildDir, fmt.Sprintf("ctx-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(work)

	if err := os.WriteFile(filepath.Join(work, "Dockerfile"), dfData, 0o644); err != nil {
		return err
	}
	// Stage the model into the build context (skipped for self-contained
	// families). Log it so the (potentially slow, multi-GB) copy never looks frozen.
	if !selfContained(family) {
		var stageGB float64
		if fi, err := os.Stat(modelPath); err == nil {
			stageGB = float64(fi.Size()) / 1e9
		}
		log(fmt.Sprintf("Staging model into build context (%.2f GB)…", stageGB))
		staged, linked, err := linkOrCopyReport(modelPath, filepath.Join(work, "model.gguf"))
		if err != nil {
			return fmt.Errorf("stage model into build context: %w", err)
		}
		if linked {
			log("Staged model (hardlinked, instant)")
		} else {
			log(fmt.Sprintf("Staged model (copied %.2f GB in %s)", stageGB, staged.Round(time.Millisecond)))
		}
	}

	// The video family bakes several extra weights (VAE, T5 encoder, high-noise
	// diffusion model). Stage each under extra/ and write a manifest the videogate
	// reads at runtime to map files to sd-cli flags.
	if family == "video" {
		extras, err := b.EnsureExtraFiles(ctx, o.Model, log)
		if err != nil {
			return err
		}
		extraDir := filepath.Join(work, "extra")
		if err := os.MkdirAll(extraDir, 0o755); err != nil {
			return err
		}
		manifest := make([]map[string]string, 0, len(extras))
		for _, w := range extras {
			if _, _, err := linkOrCopyReport(w.Path, filepath.Join(extraDir, w.File)); err != nil {
				return fmt.Errorf("stage extra weight %s: %w", w.File, err)
			}
			manifest = append(manifest, map[string]string{"file": w.File, "role": w.Role})
			log("Staged extra weight: " + w.File)
		}
		mf, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(work, "extra_files.json"), mf, 0o644); err != nil {
			return err
		}
		// Stage the videogate shim source so the Dockerfile's `gate` stage compiles it.
		if err := stageVideogate(b.BaseDir, work); err != nil {
			return err
		}
	}

	// The llama.cpp family fronts the model with the llmgate shim and bakes a
	// system prompt + optional vision projector. Other families (sd/whisper/tts)
	// run their own server directly and need none of these, so skip them.
	if family == "llama" {
		// Bake the optional initialization prompt (always present, possibly empty
		// so the Dockerfile's COPY never fails; an empty prompt = pure passthrough).
		if err := os.WriteFile(filepath.Join(work, "system_prompt.txt"), []byte(o.SystemPrompt), 0o644); err != nil {
			return err
		}
		if strings.TrimSpace(o.SystemPrompt) != "" {
			log(fmt.Sprintf("Baking initialization prompt (%d chars, mode=%s)", len(o.SystemPrompt), injectMode(o.InjectMode)))
		}

		// Stage the optional vision projector (mmproj). Always create the file so
		// the Dockerfile's COPY never fails: a real hardlink for vision models, an
		// empty placeholder otherwise — llmgate ignores a zero-byte mmproj.
		mmDst := filepath.Join(work, "mmproj.gguf")
		mmPath, err := b.EnsureMMProj(ctx, o.Model, log)
		if err != nil {
			return err
		}
		if mmPath != "" {
			if _, _, err := linkOrCopyReport(mmPath, mmDst); err != nil {
				return fmt.Errorf("stage mmproj into build context: %w", err)
			}
			log("Staged vision projector (mmproj.gguf)")
		} else if err := os.WriteFile(mmDst, nil, 0o644); err != nil {
			return err
		}

		// Stage the llmgate shim source so the Docker `gate` stage can compile it.
		if err := stageGate(b.BaseDir, work); err != nil {
			return err
		}
	}

	// The python family runs a PyTorch model behind the pygate shim (interpreted,
	// not compiled). Stage its source so Dockerfile.python copies it in.
	if family == "python" {
		if err := stagePygate(b.BaseDir, work); err != nil {
			return err
		}
	}

	ref := o.Ref()
	log(fmt.Sprintf("Building %s [%s/%s] from %s ...", ref, engine, compute, o.Model.Name))
	buildArgs := []string{
		"build", "-t", ref,
		"--build-arg", "INJECT_MODE=" + injectMode(o.InjectMode),
		"--build-arg", "MODALITY=" + o.Model.Mod(),
		"--label", "local-llm.tool=builder",
		"--label", "local-llm.modality=" + o.Model.Mod(),
		"--label", "local-llm.model=" + o.Model.ID,
		"--label", "local-llm.runtime=" + o.Model.Rt(),
		"--label", "local-llm.compute=" + compute,
		// Engine the image was built with — Run reads this so a podman-built image
		// is launched with podman (and vice versa) without the UI having to track it.
		"--label", "local-llm.engine=" + engine,
		// Capture the full creation config so the UI can clone an image as a
		// template (system prompt may be multiline — exec passes it verbatim).
		"--label", "local-llm.inject_mode=" + injectMode(o.InjectMode),
		"--label", "local-llm.system_prompt=" + o.SystemPrompt,
		// Deploy defaults (applied by Run, surfaced for clone-as-template).
		"--label", fmt.Sprintf("local-llm.ctx=%d", o.CtxSize),
		"--label", fmt.Sprintf("local-llm.memory=%g", o.MemoryGB),
		"--label", "local-llm.route=" + o.Route,
		"--label", fmt.Sprintf("local-llm.autostart=%t", o.Autostart),
	}
	if o.CtxSize > 0 {
		buildArgs = append(buildArgs, "--build-arg", fmt.Sprintf("CTX_SIZE=%d", o.CtxSize))
	}
	// Podman remote (macOS GPU path): the client forwards its default seccomp
	// profile *path* (/etc/containers/seccomp.json on Alpine) to the machine VM,
	// which ships the profile at /usr/share/containers/seccomp.json instead, so
	// the RUN steps fail to open it. The build just compiles llama.cpp, so run it
	// seccomp-unconfined to sidestep the path mismatch. Docker is unaffected.
	if engine == enginePodman {
		buildArgs = append(buildArgs, "--security-opt", "seccomp=unconfined")
	}
	buildArgs = append(buildArgs, work)
	if err := runStreaming(ctx, b.BaseDir, engine, log, buildArgs...); err != nil {
		return fmt.Errorf("%s build failed: %w", engine, err)
	}
	log("Built image: " + ref)

	if o.ExportTar {
		safe := strings.NewReplacer("/", "_", ":", "_").Replace(ref)
		out := filepath.Join(b.ImagesDir, safe+".tar")
		log("Exporting to images/" + filepath.Base(out) + " ...")
		if err := runStreaming(ctx, b.BaseDir, engine, log, "save", "-o", out, ref); err != nil {
			return fmt.Errorf("%s save failed: %w", engine, err)
		}
		if fi, err := os.Stat(out); err == nil {
			log(fmt.Sprintf("Exported %.2f GB: images/%s", float64(fi.Size())/1e9, filepath.Base(out)))
		}
	}

	// Reclaim the now-dangling previous build: rebuilding over :latest orphans the
	// old multi-GB model image as <none>, which otherwise accumulates per rebuild.
	// Scope strictly to our tool label (and dangling-only — no -a) so it can never
	// touch the user's other images or the active build cache. Best-effort.
	if out, err := engineCmd(ctx, engine, "image", "prune", "-f", "--filter", "label=local-llm.tool=builder").CombinedOutput(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" && !strings.Contains(s, "reclaimed space: 0B") {
			log("Reclaimed dangling layers from previous builds.")
		}
	}
	return nil
}

// BuildEnsemble assembles an Ensemble image: the Conductor (ensemblegate) plus a
// resolved manifest. Orchestrated images run their specialists as sibling
// containers via the host engine; embedded (mega) baking is a future path. It
// assigns each member a host port, synthesises a conductor member for tool-calling,
// and bakes the manifest. modelHost is where the started specialists are reachable
// (e.g. host.docker.internal when the Ensemble runs in a container).
func (b *Builder) BuildEnsemble(ctx context.Context, e ensemble.Ensemble, imageName, tag, engine string, exportTar bool, modelHost string, log LogFunc) (string, error) {
	eng := resolveEngine(engine)
	if tag == "" {
		tag = "latest"
	}
	ref := imageName + ":" + tag

	mani := ensemble.Manifest{Ensemble: e, ModelHost: modelHost}
	if mani.ModelHost == "" {
		mani.ModelHost = "host.docker.internal"
	}
	if mani.Engine == "" {
		mani.Engine = eng
	}
	port := 8101
	for i := range mani.Members {
		if mani.Members[i].Port == 0 {
			mani.Members[i].Port = port
		}
		port++
	}
	if e.Routing == "tool-calling" && e.Conductor != "" {
		mani.Members = append(mani.Members, ensemble.Member{Tool: "_conductor", Modality: "text", Image: e.Conductor, Port: 8100, VRAMGB: 4})
	}

	work := filepath.Join(b.BuildDir, fmt.Sprintf("ens-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	df, err := os.ReadFile(filepath.Join(b.BaseDir, "docker", "Dockerfile.ensemble"))
	if err != nil {
		return "", fmt.Errorf("read Dockerfile.ensemble: %w", err)
	}
	if err := os.WriteFile(filepath.Join(work, "Dockerfile"), df, 0o644); err != nil {
		return "", err
	}
	if err := stageEnsemblegate(b.BaseDir, work); err != nil {
		return "", err
	}
	mb, _ := json.MarshalIndent(mani, "", "  ")
	if err := os.WriteFile(filepath.Join(work, "manifest.json"), mb, 0o644); err != nil {
		return "", err
	}

	log(fmt.Sprintf("Building ensemble %s [%s, %s mode, %d members] ...", ref, eng, e.PackageMode, len(e.Members)))
	args := []string{
		"build", "-t", ref,
		"--label", "local-llm.tool=builder",
		"--label", "local-llm.kind=ensemble",
		"--label", "local-llm.ensemble=" + e.ID,
		"--label", "local-llm.compute=" + e.Compute,
		"--label", "local-llm.engine=" + eng,
	}
	if eng == enginePodman {
		args = append(args, "--security-opt", "seccomp=unconfined")
	}
	args = append(args, work)
	if err := runStreaming(ctx, b.BaseDir, eng, log, args...); err != nil {
		return "", fmt.Errorf("%s build failed: %w", eng, err)
	}
	log("Built ensemble image: " + ref)

	if exportTar {
		safe := strings.NewReplacer("/", "_", ":", "_").Replace(ref)
		out := filepath.Join(b.ImagesDir, safe+".tar")
		log("Exporting to images/" + filepath.Base(out) + " ...")
		if err := runStreaming(ctx, b.BaseDir, eng, log, "save", "-o", out, ref); err != nil {
			return "", fmt.Errorf("%s save failed: %w", eng, err)
		}
	}
	return ref, nil
}

// RunOptions configures starting a container from a built image.
type RunOptions struct {
	Ref       string
	HostPort  int
	Engine    string  // "docker" (default) | "podman"
	Compute   string  // "cuda" adds --gpus all; "vulkan" exposes the paravirt GPU
	MemoryGB  float64 // >0 sets --memory
	Route     string  // local URL alias (stored as a label for the proxy)
	Autostart bool    // adds --restart unless-stopped (starts with Docker Desktop)
	// Orchestrator mounts the host docker socket + host.docker.internal so an
	// Ensemble's Conductor can start/stop sibling specialist containers.
	Orchestrator bool
	// Optional init-prompt override applied at run time (no rebuild). llmgate
	// prefers the SYSTEM_PROMPT env over the baked file, so setting these swaps a
	// model's behavior for a ~10s container restart instead of a recompile.
	SystemPrompt string // overrides the baked system prompt when non-empty
	InjectMode   string // "missing" | "always"; overrides the baked mode when set
}

// Run starts a container detached and returns its id.
func (b *Builder) Run(ctx context.Context, o RunOptions) (string, error) {
	if o.HostPort == 0 {
		o.HostPort = 8080
	}
	engine := resolveEngine(o.Engine)
	name := fmt.Sprintf("localllm-%d", o.HostPort)
	// Best-effort removal of a stale container occupying the same name/port.
	_ = engineCmd(ctx, engine, "rm", "-f", name).Run()

	args := []string{
		"run", "-d", "--name", name,
		// Bind the published port to loopback only so the model isn't reachable
		// from the local network (no auth sits in front of it). The managed Caddy
		// proxy still reaches it via host.docker.internal, which Docker Desktop
		// routes to the host's loopback.
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", o.HostPort),
		"--label", "local-llm.tool=runtime",
		"--label", "local-llm.ref=" + o.Ref,
		"--label", "local-llm.route=" + o.Route,
		"--label", "local-llm.engine=" + engine,
	}
	switch o.Compute {
	case "cuda":
		args = append(args, "--gpus", "all")
	case "vulkan":
		// Paravirtualized GPU: a libkrun/krunkit Podman machine (macOS) or a host
		// GPU on Linux exposes the DRI render node. llama.cpp's Vulkan backend then
		// finds the (virtio) device. Requires Podman+libkrun on macOS — see README.
		args = append(args, "--device", "/dev/dri")
	}
	if o.MemoryGB > 0 {
		// MB so fractional GB (e.g. 4.5) is accepted by the engine.
		args = append(args, "--memory", fmt.Sprintf("%dm", int(o.MemoryGB*1024)))
	}
	if o.Orchestrator {
		// An Ensemble's Conductor manages sibling specialist containers via the host
		// engine and reaches them on the host, so mount the socket + host gateway.
		args = append(args, "-v", "/var/run/docker.sock:/var/run/docker.sock",
			"--add-host", "host.docker.internal:host-gateway")
	}
	if o.Autostart {
		args = append(args, "--restart", "unless-stopped")
	}
	// Same podman-remote seccomp path mismatch as Build (see there): the client
	// forwards a profile path the machine VM lacks. Run the model server
	// seccomp-unconfined on Podman; it's trusted local llama.cpp. label=disable
	// turns off SELinux confinement so the container can reach the GPU at /dev/dri
	// on the (SELinux-enforcing) libkrun machine — the Venus/krunkit GPU path needs
	// it (matches RamaLama). Both are no-ops/irrelevant on Docker.
	if engine == enginePodman {
		args = append(args, "--security-opt", "seccomp=unconfined", "--security-opt", "label=disable")
	}
	// Run-time init-prompt override (no rebuild): llmgate reads SYSTEM_PROMPT from
	// the env in preference to the baked file. A single arg is passed verbatim by
	// exec, so a multi-line prompt is safe (no shell interpolation).
	if strings.TrimSpace(o.SystemPrompt) != "" {
		args = append(args, "-e", "SYSTEM_PROMPT="+o.SystemPrompt)
		if o.InjectMode != "" {
			args = append(args, "-e", "INJECT_MODE="+o.InjectMode)
		}
		// Record that this instance overrides its baked prompt so the UI can show it,
		// plus the persona name parsed from the override ("You are FinBot…" → FinBot)
		// as a short, comma-free label. The chat uses this to name the sender per
		// instance — so two instances with different overrides read as different
		// people, instead of both falling back to the port.
		args = append(args, "--label", "local-llm.prompt_override=true")
		if p := personaName(o.SystemPrompt); p != "" {
			args = append(args, "--label", "local-llm.persona="+p)
		}
	}
	args = append(args, o.Ref)

	out, err := engineCmd(ctx, engine, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s run: %v: %s", engine, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// personaName pulls a persona out of a system prompt ("You are FinBot, a terse…"
// → "FinBot"), mirroring the frontend's extraction. Returns "" for a generic or
// nameless prompt. Kept deliberately simple: a capitalized token after "you are"
// (or "your name is"), trimmed of trailing punctuation.
var personaRes = []*regexp.Regexp{
	regexp.MustCompile(`[Yy]ou are\s+(?:called\s+)?([A-Z][A-Za-z0-9._-]{1,30})`),
	regexp.MustCompile(`[Yy]our name is\s+([A-Z][A-Za-z0-9._-]{1,30})`),
}

func personaName(prompt string) string {
	for _, re := range personaRes {
		if m := re.FindStringSubmatch(prompt); m != nil {
			n := strings.TrimRight(m[1], ".,;:!?'\"")
			switch strings.ToLower(n) {
			case "", "a", "an", "the", "your":
			default:
				return n
			}
		}
	}
	return ""
}

// Stop force-removes a container by id or name on the given engine.
func (b *Builder) Stop(ctx context.Context, engine, id string) error {
	// Retry the flaky socket: Stop is the start of the burst (rm -> regen-proxy
	// -> list-refresh) that used to surface "exit status 125" when one rapid
	// connection got refused.
	var out []byte
	var err error
	for attempt := 0; ; attempt++ {
		out, err = engineCmd(ctx, engine, "rm", "-f", id).CombinedOutput()
		if err == nil || attempt >= engineRetries {
			break
		}
		// CombinedOutput merges stderr into out, so check the text directly.
		if !transientEngineErr(err) && !strings.Contains(strings.ToLower(string(out)), "connection refused") {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(120*(attempt+1)) * time.Millisecond):
		}
	}
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SysInfo reports the active engine machine's resources so the UI can show
// "fits your system" guidance. Best-effort: zero/empty fields when undeterminable.
type SysInfo struct {
	MemGB  float64 `json:"mem_gb"`
	CPUs   int     `json:"cpus"`
	GPU    string  `json:"gpu"`     // "" | "vulkan" | "cuda"
	VRAMGB float64 `json:"vram_gb"` // total GPU VRAM (GB), 0 if unknown
	Engine string  `json:"engine"`  // engine the numbers came from
}

// SysInfo reports where models actually run. It prefers podman (the GPU path on
// macOS) over docker, reading each engine's machine memory/CPUs via the remote
// `info` API (which works from inside the containerized factory, unlike the
// host-only `machine inspect`). GPU is taken from FACTORY_GPU, which start.sh sets
// when it detects a GPU-capable Podman machine.
func (b *Builder) SysInfo(ctx context.Context) SysInfo {
	si := SysInfo{GPU: os.Getenv("FACTORY_GPU")}
	if v, err := strconv.ParseFloat(os.Getenv("FACTORY_VRAM"), 64); err == nil {
		si.VRAMGB = v
	}
	for _, engine := range candidateEngines() {
		// podman exposes host RAM/CPU under .Host; docker at the top level.
		out, err := engineCmd(ctx, engine, "info", "--format", "{{.Host.MemTotal}} {{.Host.NCPU}}").Output()
		if err != nil || strings.Contains(string(out), "<no value>") {
			out, err = engineCmd(ctx, engine, "info", "--format", "{{.MemTotal}} {{.NCPU}}").Output()
		}
		if err != nil {
			continue
		}
		var memBytes int64
		var cpus int
		fmt.Sscan(strings.TrimSpace(string(out)), &memBytes, &cpus)
		if memBytes <= 0 {
			continue
		}
		si.MemGB = float64(memBytes) / 1e9
		si.CPUs = cpus
		si.Engine = engine
		if engine == enginePodman {
			return si // podman is the GPU path — prefer its numbers
		}
	}
	return si
}

// EngineVersions probes each installed engine's server version. An empty value
// means the engine is present but unreachable (e.g. the Podman machine/socket is
// down) — which is exactly the state that surfaces as "exit status 125" in normal
// calls. Drives the /readyz endpoint and the UI's degraded banner.
func (b *Builder) EngineVersions(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, engine := range candidateEngines() {
		v, _ := engineCmd(ctx, engine, "version", "--format", "{{.Server.Version}}").Output()
		out[engine] = strings.TrimSpace(string(v))
	}
	return out
}

// RecoverOnStartup brings the deployment back after a host/VM reboot: it restarts
// any stopped containers that were built with autostart=true, then regenerates the
// proxy from whatever is running. Best-effort and non-fatal — on a fresh factory
// with nothing to recover it's a no-op. (Podman's unless-stopped restart policy
// doesn't reliably fire after a full machine stop, and the Caddy proxy needs its
// routes rebuilt, so the factory handles both itself.)
func (b *Builder) RecoverOnStartup(ctx context.Context, log LogFunc) {
	conts, err := b.Containers(ctx)
	if err != nil {
		return
	}
	for _, c := range conts {
		if s, _ := c["State"].(string); s == "running" {
			continue
		}
		labels, _ := c["Labels"].(string)
		if labelValue(labels, "local-llm.autostart") != "true" {
			continue
		}
		name, _ := c["Names"].(string)
		engine, _ := c["Engine"].(string)
		if name == "" {
			continue
		}
		if err := engineCmd(ctx, engine, "start", name).Run(); err == nil {
			log("restarted autostart container after boot: " + name)
		}
	}
	if err := b.RegenProxy(ctx); err != nil {
		log("regen proxy on startup: " + err.Error())
	}
}

// ContainerLogs returns the last `tail` lines of a container's logs (stdout+stderr
// merged). Lets the UI show why a container vanished/crashed instead of silently
// dropping it from the list.
func (b *Builder) ContainerLogs(ctx context.Context, engine, id string, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}
	out, err := engineCmd(ctx, resolveEngine(engine), "logs", "--tail", fmt.Sprintf("%d", tail), id).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ----------------------------------------------------------------------------
// Local-URL reverse proxy (Caddy)
//
// A single managed Caddy container routes http://<alias>.localhost to each
// running model container (by its published host port). *.localhost already
// resolves to 127.0.0.1 in browsers, so no hosts-file edit is needed. The
// Caddyfile is regenerated from the running containers' route labels; Caddy
// runs with --watch so it hot-reloads when the file changes.
// ----------------------------------------------------------------------------

const proxyContainer = "local-llm-proxy"

// ProxyPort is the host port the proxy listens on (clean URLs need 80).
func (b *Builder) ProxyPort() string {
	if p := os.Getenv("PROXY_PORT"); p != "" {
		return p
	}
	return "80"
}

// RegenProxy rebuilds the Caddyfile from running model containers that have a
// route alias and ensures the proxy is up (or torn down if there are no routes).
func (b *Builder) RegenProxy(ctx context.Context) error {
	conts, err := b.Containers(ctx)
	if err != nil {
		return err
	}
	type route struct{ alias, port string }
	var routes []route
	for _, c := range conts {
		if s, _ := c["State"].(string); s != "running" {
			continue
		}
		labels, _ := c["Labels"].(string)
		alias := strings.TrimSpace(labelValue(labels, "local-llm.route"))
		if alias == "" {
			continue
		}
		name, _ := c["Names"].(string)
		port := portFromName(name)
		if port != "" {
			routes = append(routes, route{alias, port})
		}
	}

	var sb strings.Builder
	if len(routes) == 0 {
		fmt.Fprintf(&sb, ":%s {\n\trespond \"local-llm proxy: no models routed\" 200\n}\n", b.ProxyPort())
	} else {
		for _, r := range routes {
			fmt.Fprintf(&sb, "http://%s.localhost {\n\treverse_proxy host.docker.internal:%s\n}\n", r.alias, r.port)
		}
	}
	if err := os.MkdirAll(b.ConfigDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(b.ConfigDir, "Caddyfile"), []byte(sb.String()), 0o644); err != nil {
		return err
	}

	engine := proxyEngine()
	if len(routes) == 0 {
		_ = engineCmd(ctx, engine, "rm", "-f", proxyContainer).Run() // free the port
		return nil
	}
	return b.ensureProxy(ctx, engine)
}

func (b *Builder) ensureProxy(ctx context.Context, engine string) error {
	out, _ := engineCmd(ctx, engine, "ps", "-q", "-f", "name=^"+proxyContainer+"$").Output()
	if strings.TrimSpace(string(out)) != "" {
		return nil // already running; --watch picks up the regenerated Caddyfile
	}
	_ = engineCmd(ctx, engine, "rm", "-f", proxyContainer).Run()

	caddyfileHost := strings.ReplaceAll(b.HostDir+"/config/Caddyfile", "\\", "/")
	args := []string{
		"run", "-d", "--name", proxyContainer,
		"--restart", "unless-stopped",
		"-p", b.ProxyPort() + ":80",
		"-v", caddyfileHost + ":/etc/caddy/Caddyfile",
		"--add-host", "host.docker.internal:host-gateway",
		"--label", "local-llm.tool=proxy",
		"caddy:2-alpine",
		"caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile", "--watch",
	}
	if cmdOut, err := engineCmd(ctx, engine, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("start proxy: %v: %s", err, strings.TrimSpace(string(cmdOut)))
	}
	return nil
}

// labelValue extracts a value from docker's comma-joined "k=v,k=v" Labels field.
func labelValue(labels, key string) string {
	for _, part := range strings.Split(labels, ",") {
		if i := strings.Index(part, "="); i > 0 && part[:i] == key {
			return part[i+1:]
		}
	}
	return ""
}

// portFromName extracts the host port from a "localllm-<port>" container name.
func portFromName(name string) string {
	if i := strings.LastIndex(name, "-"); i >= 0 {
		return name[i+1:]
	}
	return ""
}

// Images lists images built by this tool, enriched with each image's compute
// label ("cpu"/"cuda"). We pass -a so orphaned/untagged images (left behind by
// rebuilding over a tag) still appear — otherwise they'd be invisible and
// un-deletable, silently eating disk. The UI shows them as "<untagged>".
// NOTE: `docker images --format` does not expose labels, so we resolve the
// compute type per image via inspect.
func (b *Builder) Images(ctx context.Context) ([]map[string]any, error) {
	var all []map[string]any
	var firstErr error
	anyOK := false
	for _, engine := range candidateEngines() {
		imgs, err := dockerJSONLines(ctx, engine, "images", "-a", "--filter", "label=local-llm.tool=builder", "--format", "{{json .}}")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue // engine present but unusable (e.g. podman machine stopped) — skip it
		}
		anyOK = true
		for _, im := range imgs {
			im["Engine"] = engine
			normalizeImageFields(im)
			if id, _ := im["ID"].(string); id != "" {
				// `docker images` omits labels, which left the UI unable to read an
				// image's modality (everything showed "Chat") and other baked config.
				// One inspect resolves all of them; populate Labels as the comma-joined
				// "k=v,k=v" string the UI parses, and derive Compute from the same map
				// (falling back to the dedicated lookup only if the label is absent).
				labels, _ := b.ImageLabels(ctx, engine, id)
				if len(labels) > 0 {
					keys := make([]string, 0, len(labels))
					for k := range labels {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					parts := make([]string, 0, len(keys))
					for _, k := range keys {
						parts = append(parts, k+"="+labels[k])
					}
					im["Labels"] = strings.Join(parts, ",")
				}
				if c := labels["local-llm.compute"]; c != "" {
					im["Compute"] = c
				} else {
					im["Compute"] = b.ImageCompute(ctx, engine, id)
				}
			}
			all = append(all, im)
		}
	}
	// Only fail when EVERY engine was unreachable. If a healthy engine answered
	// (even with zero rows), return that — a dead Podman must never blank a
	// healthy Docker's table (or vice versa).
	if all == nil {
		if !anyOK && firstErr != nil {
			return nil, firstErr
		}
		return []map[string]any{}, nil
	}
	return all, nil
}

// normalizeImageFields makes a podman `images --format {{json .}}` record look
// like docker's, which the UI (and the compute enrichment above) assume: docker
// emits Repository, Tag, ID, and a human-readable Size string; podman emits
// Names/RepoTags, "Id", and a numeric Size (bytes). Left unnormalized, podman
// images render as blank "<untagged>" rows with dead Run/Delete buttons (ref is
// built from a missing ID) and no compute badge — and a single such row can
// abort the whole table render. Docker records already have these fields, so
// this is a no-op for them.
func normalizeImageFields(im map[string]any) {
	if s, _ := im["ID"].(string); s == "" {
		if id, _ := im["Id"].(string); id != "" {
			im["ID"] = id
		}
	}
	if s, _ := im["Repository"].(string); s == "" {
		if repo, tag := firstRepoTag(im); repo != "" {
			im["Repository"] = repo
			im["Tag"] = tag
		}
	}
	// podman gives Size as bytes (a JSON number → float64); render the same
	// decimal-unit human string docker prints.
	if n, ok := im["Size"].(float64); ok {
		im["Size"] = humanSize(int64(n))
	}
}

// firstRepoTag pulls the first real "repo:tag" reference out of a podman image
// record (RepoTags, then Names), splitting it into repository and tag. The
// "localhost/" prefix podman adds to locally-built images is kept so the ref the
// UI derives (repo:tag) still resolves with `podman run`/`rmi`.
func firstRepoTag(im map[string]any) (repo, tag string) {
	for _, key := range []string{"RepoTags", "Names"} {
		arr, ok := im[key].([]any)
		if !ok {
			continue
		}
		for _, e := range arr {
			s, _ := e.(string)
			s = strings.TrimSpace(s)
			if s == "" || s == "<none>:<none>" {
				continue
			}
			// Split on the last colon, but only if what follows isn't a path
			// segment (guards against a registry "host:port/repo" with no tag).
			if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[i+1:], "/") {
				return s[:i], s[i+1:]
			}
			return s, "latest"
		}
	}
	return "", ""
}

// humanSize formats a byte count the way docker's `images` column does: decimal
// (1000-based) units with two decimals, e.g. 9465678461 -> "9.47 GB".
func humanSize(n int64) string {
	const unit = 1000.0
	f := float64(n)
	units := []string{"B", "kB", "MB", "GB", "TB", "PB"}
	i := 0
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	return fmt.Sprintf("%.2f %s", f, units[i])
}

// normalizeContainerFields does for `ps` records what normalizeImageFields does
// for images: reshape podman's JSON to the docker form the UI assumes. Docker
// emits Names/Ports as strings and Labels as a "k=v,k=v" string; podman emits
// Names/Ports as arrays and Labels as an object. The Labels case is the worst —
// the UI does `c.Labels.split(",")` to find the local-llm.ref, which throws on an
// object and blanks the table. No-op for docker records (already strings).
func normalizeContainerFields(c map[string]any) {
	if s, _ := c["ID"].(string); s == "" {
		if id, _ := c["Id"].(string); id != "" {
			c["ID"] = id
		}
	}
	if arr, ok := c["Names"].([]any); ok {
		c["Names"] = joinStrings(arr, ",")
	}
	if m, ok := c["Labels"].(map[string]any); ok {
		c["Labels"] = labelsToString(m)
	}
	if arr, ok := c["Ports"].([]any); ok {
		c["Ports"] = portsToString(arr)
	}
}

// joinStrings joins the string elements of a []any with sep (non-strings skipped).
func joinStrings(arr []any, sep string) string {
	parts := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, sep)
}

// labelsToString renders a podman Labels map as docker's "k=v,k=v" string, sorted
// for stable output. The UI only needs to find local-llm.ref by splitting on ",".
func labelsToString(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, ",")
}

// portsToString renders podman's array of port mappings as a docker-style string
// (e.g. "0.0.0.0:8080->8080/tcp"). The UI's port fallback regex only needs the
// ":<hostPort>->" substring; the container name (localllm-<port>) is the primary
// source, so this is best-effort.
func portsToString(arr []any) string {
	parts := make([]string, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		hostIP, _ := m["host_ip"].(string)
		if hostIP == "" {
			hostIP = "0.0.0.0"
		}
		hostPort := numField(m, "host_port")
		ctrPort := numField(m, "container_port")
		proto, _ := m["protocol"].(string)
		if proto == "" {
			proto = "tcp"
		}
		parts = append(parts, fmt.Sprintf("%s:%d->%d/%s", hostIP, hostPort, ctrPort, proto))
	}
	return strings.Join(parts, ", ")
}

// numField reads a JSON number field (unmarshaled as float64) as an int.
func numField(m map[string]any, key string) int {
	if f, ok := m[key].(float64); ok {
		return int(f)
	}
	return 0
}

// SaveImage streams `<engine> save <ref>` (the image tarball) to w. Used to let a
// browser download a built image to a location of the user's choosing.
func (b *Builder) SaveImage(ctx context.Context, engine, ref string, w io.Writer) error {
	cmd := engineCmd(ctx, engine, "save", ref)
	cmd.Stdout = w
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s save: %v: %s", resolveEngine(engine), err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// ImageExists reports whether an image ref is present in the given engine.
func (b *Builder) ImageExists(ctx context.Context, engine, ref string) bool {
	return engineCmd(ctx, engine, "image", "inspect", ref).Run() == nil
}

// ImageCompute returns the "local-llm.compute" label baked into an image
// ("cpu"/"cuda"/"vulkan"), or "" if it can't be determined. This is the
// authoritative source for the GPU run flags.
func (b *Builder) ImageCompute(ctx context.Context, engine, ref string) string {
	out, err := engineCmd(ctx, engine, "image", "inspect",
		"--format", `{{index .Config.Labels "local-llm.compute"}}`, ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ImageLabels returns all labels on an image (used to clone its build config).
// engine "" resolves to docker; for a ref that may live on either engine, pass
// the engine from the image row (annotated by Images).
func (b *Builder) ImageLabels(ctx context.Context, engine, ref string) (map[string]string, error) {
	out, err := engineCmd(ctx, engine, "image", "inspect",
		"--format", "{{json .Config.Labels}}", ref).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", ref, err)
	}
	var labels map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(out), &labels); err != nil {
		return nil, err
	}
	return labels, nil
}

// Containers lists (running + stopped) containers started by this tool, across
// every available engine. Each row is annotated with its Engine so Stop routes
// to the right one.
func (b *Builder) Containers(ctx context.Context) ([]map[string]any, error) {
	var all []map[string]any
	var firstErr error
	anyOK := false
	for _, engine := range candidateEngines() {
		cs, err := dockerJSONLines(ctx, engine, "ps", "-a", "--filter", "label=local-llm.tool=runtime", "--format", "{{json .}}")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		anyOK = true
		for _, c := range cs {
			c["Engine"] = engine
			normalizeContainerFields(c)
			all = append(all, c)
		}
	}
	// Only fail when EVERY engine was unreachable (see Images for rationale).
	if all == nil {
		if !anyOK && firstErr != nil {
			return nil, firstErr
		}
		return []map[string]any{}, nil
	}
	return all, nil
}

// RemoveImage removes a built image (<engine> rmi -f) and, if alsoTar, its
// exported tarball under ./images.
func (b *Builder) RemoveImage(ctx context.Context, engine, ref string, alsoTar bool) error {
	out, err := engineCmd(ctx, engine, "rmi", "-f", ref).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s rmi: %v: %s", resolveEngine(engine), err, strings.TrimSpace(string(out)))
	}
	if alsoTar {
		safe := strings.NewReplacer("/", "_", ":", "_").Replace(ref)
		_ = os.Remove(filepath.Join(b.ImagesDir, safe+".tar"))
	}
	return nil
}

// ModelOnDisk reports whether a model's weights are present and their byte size.
func (b *Builder) ModelOnDisk(m catalog.Model) (bool, int64) {
	if m.File == "" {
		return false, 0 // self-contained family (e.g. tts) — nothing to download
	}
	fi, err := os.Stat(b.ModelPath(m))
	if err != nil || fi.Size() == 0 {
		return false, 0
	}
	total := fi.Size()
	// Vision models aren't usable until their projector is also present; report
	// not-ready (but still count the weights toward on-disk size) if it's missing.
	if m.MMProjFile != "" {
		mf, mErr := os.Stat(b.MMProjPath(m))
		if mErr != nil || mf.Size() == 0 {
			return false, total
		}
		total += mf.Size()
	}
	return true, total
}

// RemoveModel deletes a model's downloaded weights from ./models (no-op if absent).
func (b *Builder) RemoveModel(m catalog.Model) error {
	if err := os.Remove(b.ModelPath(m)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// engineRetries is how many extra times a transient engine command is retried.
// The macOS Docker->Podman bind-mounted socket intermittently refuses *fresh*
// connections, especially during bursts (Stop fires rm -> regen-proxy -> list
// in quick succession). Without retry, one refused call blanks the UI ("exit
// status 125") and the image row vanishes. A few quick retries smooth that over.
const engineRetries = 4

// transientEngineErr reports whether an engine command failed with a retryable
// connection error (vs a real error like "no such container", which must NOT be
// retried — we'd just waste time and still fail).
// Engine circuit-breaker state: which engines are currently failing to connect.
// Flipped by dockerJSONLines (down on a connection error, up on any success) so
// a stopped/absent engine is fast-failed instead of retried on every poll.
var (
	engineDownMu sync.Mutex
	engineDown   = map[string]bool{}
)

func engineIsDown(engine string) bool {
	engineDownMu.Lock()
	defer engineDownMu.Unlock()
	return engineDown[engine]
}
func setEngineDown(engine string, down bool) {
	engineDownMu.Lock()
	defer engineDownMu.Unlock()
	if down {
		engineDown[engine] = true
	} else {
		delete(engineDown, engine)
	}
}

// EnginesDown reports engines that are currently unreachable AND actually
// configured here, so the UI can explain "Podman unreachable" instead of showing
// a silently-empty table. A host with the podman binary but no machine/socket
// (CONTAINER_HOST unset) is not flagged — that's expected, not a fault.
func (b *Builder) EnginesDown() []string {
	engineDownMu.Lock()
	defer engineDownMu.Unlock()
	var out []string
	for _, e := range candidateEngines() {
		if !engineDown[e] {
			continue
		}
		if e == enginePodman && os.Getenv("CONTAINER_HOST") == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

func transientEngineErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg += " " + strings.ToLower(string(ee.Stderr))
	}
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "cannot connect to podman") ||
		strings.Contains(msg, "cannot connect to the docker daemon") ||
		(strings.Contains(msg, "sock") && strings.Contains(msg, "no such file"))
}

// ClassifyEngineError maps a raw engine failure to a short, human, actionable
// message — or "" if it doesn't recognize it. Callers keep the raw error as a
// collapsible detail. This is what turns a bare "exit status 125" surfaced in the
// UI into the actual cause (engine down, port taken, out of disk/memory, …).
func ClassifyEngineError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		msg += " " + strings.ToLower(string(ee.Stderr))
	}
	contains := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(msg, s) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("connection refused", "cannot connect to podman", "cannot connect to the docker daemon"),
		strings.Contains(msg, "sock") && strings.Contains(msg, "no such file"):
		return "Container engine unreachable — the Podman machine or Docker isn't responding. Restart the factory (scripts/<your-os>/start.sh) so it reconnects, then retry."
	case contains("port is already allocated", "address already in use", "bind for"):
		return "That host port is already in use — pick a different port."
	case contains("no space left"):
		return "The engine is out of disk space — prune old images (or free disk) and retry."
	case contains("oomkilled", "signal: killed", "cannot allocate memory"):
		return "Ran out of memory — try a smaller model/quant or raise the machine's memory."
	case contains("no such container", "no such object"):
		return "That container no longer exists — refresh; it may already be gone."
	case contains("image not known", "no such image"):
		return "That image no longer exists — rebuild it or refresh the list."
	}
	return ""
}

func dockerJSONLines(ctx context.Context, engine string, args ...string) ([]map[string]any, error) {
	// Bound a single list call so a hung/absent socket can't stall a poll forever.
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	// Circuit breaker: an engine that just refused a connection is fast-failed
	// (no multi-attempt backoff) until a probe succeeds again, so a stopped or
	// absent engine — e.g. the podman binary is installed but its machine is off,
	// which is the common case — doesn't add ~5s to every poll. One probe still
	// runs each call, so recovery is detected immediately.
	retries := engineRetries
	if engineIsDown(engine) {
		retries = 0
	}
	var out []byte
	var err error
	for attempt := 0; ; attempt++ {
		out, err = engineCmd(ctx, engine, args...).Output()
		if err == nil || attempt >= retries || !transientEngineErr(err) {
			break
		}
		// Backoff a little before retrying the flaky socket; bail on cancel.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(120*(attempt+1)) * time.Millisecond):
		}
	}
	if err != nil {
		if transientEngineErr(err) {
			setEngineDown(engine, true) // trip the breaker so the next poll fast-fails
		}
		return nil, err
	}
	setEngineDown(engine, false) // answered — clear any breaker for this engine
	res := []map[string]any{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			res = append(res, m)
		}
	}
	return res, nil
}

// runStreaming runs `<engine> <args...>` and forwards combined output line-by-line.
func runStreaming(ctx context.Context, dir, engine string, log LogFunc, args ...string) error {
	cmd := engineCmd(ctx, engine, args...)
	cmd.Dir = dir
	lw := &lineWriter{log: log}
	cmd.Stdout = lw
	cmd.Stderr = lw
	err := cmd.Run()
	lw.flush()
	return err
}

func injectMode(m string) string {
	if m == "always" {
		return "always"
	}
	return "missing"
}

// stageGate copies the llmgate shim source into <work>/gate so the Dockerfile's
// `gate` stage can compile it. The shim is stdlib-only, so a minimal go.mod with
// no requires is enough (the in-container build needs no network).
func stageGate(baseDir, work string) error {
	src := filepath.Join(baseDir, "cmd", "llmgate", "main.go")
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read llmgate source (%s): %w", src, err)
	}
	gateDir := filepath.Join(work, "gate")
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gateDir, "main.go"), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(gateDir, "go.mod"), []byte("module llmgate\n\ngo 1.22\n"), 0o644)
}

// stageVideogate copies the videogate shim source into <work>/gate so the video
// Dockerfile's `gate` stage can compile it (same layout as stageGate).
func stageVideogate(baseDir, work string) error {
	src := filepath.Join(baseDir, "cmd", "videogate", "main.go")
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read videogate source (%s): %w", src, err)
	}
	gateDir := filepath.Join(work, "gate")
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gateDir, "main.go"), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(gateDir, "go.mod"), []byte("module videogate\n\ngo 1.22\n"), 0o644)
}

// stageEnsemblegate copies the Conductor (ensemblegate) source into <work>/gate so
// the ensemble Dockerfile's `gate` stage can compile it.
func stageEnsemblegate(baseDir, work string) error {
	src := filepath.Join(baseDir, "cmd", "ensemblegate", "main.go")
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read ensemblegate source (%s): %w", src, err)
	}
	gateDir := filepath.Join(work, "gate")
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gateDir, "main.go"), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(gateDir, "go.mod"), []byte("module ensemblegate\n\ngo 1.22\n"), 0o644)
}

// stagePygate copies the pygate shim (Python, interpreted) into <work>/gate so
// Dockerfile.python copies it in. No compile step — unlike the Go gates.
func stagePygate(baseDir, work string) error {
	src := filepath.Join(baseDir, "cmd", "pygate", "main.py")
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read pygate source (%s): %w", src, err)
	}
	gateDir := filepath.Join(work, "gate")
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(gateDir, "main.py"), data, 0o644)
}

// linkOrCopyReport hardlinks src->dst (cheap, same volume) and falls back to a
// copy across filesystems. It reports whether it hardlinked and how long a copy
// took (so the caller can surface progress for large files).
func linkOrCopyReport(src, dst string) (took time.Duration, linked bool, err error) {
	_ = os.Remove(dst)
	if os.Link(src, dst) == nil {
		return 0, true, nil
	}
	start := time.Now()
	in, err := os.Open(src)
	if err != nil {
		return 0, false, err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, false, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return 0, false, err
	}
	if err := out.Close(); err != nil {
		return 0, false, err
	}
	return time.Since(start), false, nil
}

// progressReader logs download progress roughly every 100 MB.
type progressReader struct {
	r          io.Reader
	total      int64
	read       int64
	lastLogged int64
	log        LogFunc
	label      string
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.read-p.lastLogged >= 100<<20 || (err == io.EOF && p.read != p.lastLogged) {
		p.lastLogged = p.read
		if p.total > 0 {
			p.log(fmt.Sprintf("  %.0f%% (%.2f / %.2f GB)",
				float64(p.read)/float64(p.total)*100, float64(p.read)/1e9, float64(p.total)/1e9))
		} else {
			p.log(fmt.Sprintf("  %.2f GB", float64(p.read)/1e9))
		}
	}
	return n, err
}

// lineWriter buffers partial writes and emits complete lines to log.
type lineWriter struct {
	log LogFunc
	buf bytes.Buffer
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			// No trailing newline yet: keep the remainder buffered.
			l.buf.Reset()
			l.buf.WriteString(line)
			break
		}
		l.log(strings.TrimRight(line, "\r\n"))
	}
	return len(p), nil
}

func (l *lineWriter) flush() {
	if l.buf.Len() > 0 {
		l.log(strings.TrimRight(l.buf.String(), "\r\n"))
		l.buf.Reset()
	}
}
