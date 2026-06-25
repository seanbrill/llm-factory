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
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourorg/local-llm/internal/catalog"
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

	log(fmt.Sprintf("Downloading %s ...", m.File))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("download failed: HTTP %d (%s)", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	pr := &progressReader{r: resp.Body, total: resp.ContentLength, log: log, label: m.File}
	if _, err := io.Copy(f, pr); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("download interrupted: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	log("Saved model to models/" + m.File)
	return dest, nil
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

	dockerfileSrc := filepath.Join(b.BaseDir, "docker", "Dockerfile."+compute)
	dfData, err := os.ReadFile(dockerfileSrc)
	if err != nil {
		return fmt.Errorf("read %s: %w", dockerfileSrc, err)
	}

	modelPath, err := b.EnsureModel(ctx, o.Model, log)
	if err != nil {
		return err
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
	// Stage the model into the build context. Log it so the (potentially slow,
	// multi-GB) copy fallback never looks like a frozen build.
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

	// Bake the optional initialization prompt (always present, possibly empty so
	// the Dockerfile's COPY never fails; an empty prompt = pure passthrough).
	if err := os.WriteFile(filepath.Join(work, "system_prompt.txt"), []byte(o.SystemPrompt), 0o644); err != nil {
		return err
	}
	if strings.TrimSpace(o.SystemPrompt) != "" {
		log(fmt.Sprintf("Baking initialization prompt (%d chars, mode=%s)", len(o.SystemPrompt), injectMode(o.InjectMode)))
	}

	// Stage the llmgate shim source so the Docker `gate` stage can compile it.
	if err := stageGate(b.BaseDir, work); err != nil {
		return err
	}

	ref := o.Ref()
	log(fmt.Sprintf("Building %s [%s/%s] from %s ...", ref, engine, compute, o.Model.Name))
	buildArgs := []string{
		"build", "-t", ref,
		"--build-arg", "INJECT_MODE=" + injectMode(o.InjectMode),
		"--label", "local-llm.tool=builder",
		"--label", "local-llm.model=" + o.Model.ID,
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
	return nil
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
		"-p", fmt.Sprintf("%d:8080", o.HostPort),
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
	if o.Autostart {
		args = append(args, "--restart", "unless-stopped")
	}
	args = append(args, o.Ref)

	out, err := engineCmd(ctx, engine, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s run: %v: %s", engine, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Stop force-removes a container by id or name on the given engine.
func (b *Builder) Stop(ctx context.Context, engine, id string) error {
	out, err := engineCmd(ctx, engine, "rm", "-f", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
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
	for _, engine := range candidateEngines() {
		imgs, err := dockerJSONLines(ctx, engine, "images", "-a", "--filter", "label=local-llm.tool=builder", "--format", "{{json .}}")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue // engine present but unusable (e.g. podman machine stopped) — skip it
		}
		for _, im := range imgs {
			im["Engine"] = engine
			if id, _ := im["ID"].(string); id != "" {
				im["Compute"] = b.ImageCompute(ctx, engine, id)
			}
			all = append(all, im)
		}
	}
	if all == nil && firstErr != nil {
		return nil, firstErr
	}
	return all, nil
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
	for _, engine := range candidateEngines() {
		cs, err := dockerJSONLines(ctx, engine, "ps", "-a", "--filter", "label=local-llm.tool=runtime", "--format", "{{json .}}")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, c := range cs {
			c["Engine"] = engine
			all = append(all, c)
		}
	}
	if all == nil && firstErr != nil {
		return nil, firstErr
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
	fi, err := os.Stat(b.ModelPath(m))
	if err != nil || fi.Size() == 0 {
		return false, 0
	}
	return true, fi.Size()
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

func dockerJSONLines(ctx context.Context, engine string, args ...string) ([]map[string]any, error) {
	out, err := engineCmd(ctx, engine, args...).Output()
	if err != nil {
		return nil, err
	}
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
