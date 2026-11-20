package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seanbrill/local-llm/internal/ensemble"
)

// Ensemble images: the Conductor (cmd/ensemblegate) plus a baked manifest. The
// members aren't in the image: the Conductor starts them as sibling containers
// through the host engine when a request needs them (docs/ENSEMBLE.md).

// ensembleGatePackages are the sources an Ensemble image compiles: the
// Conductor and the stdlib-only packages it imports.
var ensembleGatePackages = []string{"cmd/ensemblegate", "internal/ensemble", "internal/voice", "internal/agent"}

// EnsembleManifest is the manifest an Ensemble image bakes: the definition,
// normalized, with the conductor model as a member for conductor routing and
// the built-in agent tools resolved. Members keep their pinned ports (0 lets
// the engine pick a free one at start, so ensembles never collide).
func EnsembleManifest(e ensemble.Ensemble) (ensemble.Manifest, error) {
	e.Normalize()
	if err := e.Validate(); err != nil {
		return ensemble.Manifest{}, err
	}
	if e.ID == "" {
		e.ID = ensemble.Slug(e.Name)
	}
	h, err := ensemble.HarnessConfig(e.Harness)
	if err != nil {
		return ensemble.Manifest{}, err
	}
	e.Harness = h
	mani := ensemble.Manifest{Ensemble: e, ModelHost: ensemble.DefaultModelHost}
	mani.Members = append([]ensemble.Member(nil), e.Members...)
	if e.Routing == ensemble.RoutingConductor && e.Conductor != "" {
		mani.Members = append(mani.Members, ensemble.Member{Tool: ensemble.ToolConductor, Modality: "text",
			Image: e.Conductor, Runtime: "cpp", VRAMGB: 4})
	}
	// Published by registry address, so the ensemble finds its members on
	// another computer (the engine pulls an image it doesn't have).
	for i := range mani.Members {
		mani.Members[i].Image = ensemble.AtRegistry(e.Registry, mani.Members[i].Image)
	}
	return mani, nil
}

// stageEnsemblegate stages the Conductor's sources as a module under
// <work>/gate (see stageGoPackages).
func stageEnsemblegate(baseDir, work string) error {
	return stageGoPackages(baseDir, work, ensembleGatePackages...)
}

// BuildEnsemble builds an Ensemble image (imageName:tag) and returns its ref.
// The image carries the labels the factory lists and runs it by:
// local-llm.kind=ensemble, local-llm.ensemble=<id>, local-llm.modality=ensemble
// and an empty local-llm.model (so it shows with the running models).
func (b *Builder) BuildEnsemble(ctx context.Context, e ensemble.Ensemble, imageName, tag, engine string, exportTar bool, log LogFunc) (string, error) {
	eng := resolveEngine(engine)
	if tag == "" {
		tag = "latest"
	}
	ref := imageName + ":" + tag
	mani, err := EnsembleManifest(e)
	if err != nil {
		return "", err
	}
	if mani.Engine == "" {
		mani.Engine = eng
	}

	work := filepath.Join(b.BuildDir, fmt.Sprintf("ens-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	df, err := os.ReadFile(b.runtimeDockerfile("Dockerfile.ensemble"))
	if err != nil {
		return "", fmt.Errorf("read Dockerfile.ensemble: %w", err)
	}
	if err := os.WriteFile(filepath.Join(work, "Dockerfile"), df, 0o644); err != nil {
		return "", err
	}
	if err := stageEnsemblegate(b.BaseDir, work); err != nil {
		return "", err
	}
	if err := stageHarness(b.BaseDir, work, mani.Harness, log); err != nil {
		return "", err
	}
	mb, _ := json.MarshalIndent(mani, "", "  ")
	if err := os.WriteFile(filepath.Join(work, "manifest.json"), mb, 0o644); err != nil {
		return "", err
	}

	var tools []string
	for _, m := range mani.Members {
		tools = append(tools, m.Tool)
	}
	log(fmt.Sprintf("Building ensemble %s [%s, %s routing, members: %s] ...", ref, eng, mani.Routing, strings.Join(tools, ", ")))
	args := []string{
		"build", "-t", ref,
		"--label", "local-llm.tool=builder",
		"--label", ensemble.LabelKind + "=ensemble",
		"--label", ensemble.LabelEnsemble + "=" + mani.ID,
		"--label", "local-llm.model=",
		"--label", "local-llm.modality=ensemble",
		"--label", "local-llm.compute=" + mani.Compute,
		"--label", "local-llm.engine=" + eng,
	}
	// The Conductor's architecture: an amd64 ensemble built on an arm64 Mac
	// compiles under emulation (docs/RUNTIME.md).
	arch, err := PlatformArch(mani.Platform)
	if err != nil {
		return "", err
	}
	args = append(args, "--label", "local-llm.arch="+arch)
	if mani.Platform != "" {
		args = append(args, "--platform", mani.Platform)
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
		out := filepath.Join(b.ImagesDir, exportFileName(ref))
		log("Exporting to images/" + filepath.Base(out) + " ...")
		if err := runStreaming(ctx, b.BaseDir, eng, log, "save", "-o", out, ref); err != nil {
			return "", fmt.Errorf("%s save failed: %w", eng, err)
		}
	}
	return ref, nil
}

// RemoveEnsembleMembers removes the member containers an ensemble's Conductor
// started (label local-llm.ensemble-member=<id>), so stopping the ensemble
// frees their memory even when the Conductor didn't get to clean up.
func (b *Builder) RemoveEnsembleMembers(ctx context.Context, engine, ensID string) error {
	if strings.TrimSpace(ensID) == "" {
		return nil
	}
	out, err := engineCmd(ctx, engine, "ps", "-a", "-q", "--filter", "label="+ensemble.LabelMember+"="+ensID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("list %s members: %v: %s", ensID, err, strings.TrimSpace(string(out)))
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	if out, err := engineCmd(ctx, engine, append([]string{"rm", "-f"}, ids...)...).CombinedOutput(); err != nil {
		return fmt.Errorf("remove %s members: %v: %s", ensID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsembleOfContainer returns the ensemble id a container runs ("" for a
// container that isn't an ensemble's Conductor, or can't be inspected).
func (b *Builder) EnsembleOfContainer(ctx context.Context, engine, id string) string {
	out, err := engineCmd(ctx, engine, "inspect", "-f", `{{index .Config.Labels "`+ensemble.LabelEnsemble+`"}}`, id).Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	if v == "<no value>" {
		return ""
	}
	return v
}

// orchestratorArgs are the run flags an Ensemble's Conductor needs to start
// its members through the engine and reach their published ports. Docker:
// its socket and host.docker.internal. Podman: its own Docker-compatible
// socket inside the machine (socket, from `podman info`), SELinux labelling
// off so the Conductor may use it, and the members reached on
// host.containers.internal, the name Podman gives the host.
func orchestratorArgs(engine, socket string) []string {
	if engine != enginePodman {
		return []string{"-v", "/var/run/docker.sock:/var/run/docker.sock", "--add-host", "host.docker.internal:host-gateway"}
	}
	if socket == "" {
		socket = defaultPodmanSocket
	}
	return []string{"-v", socket + ":/var/run/docker.sock", "--security-opt", "label=disable",
		"-e", "MODEL_HOST=host.containers.internal"}
}

// defaultPodmanSocket is a rootful Podman machine's API socket.
const defaultPodmanSocket = "/run/podman/podman.sock"

// podmanSocket is the Podman API socket's path where containers run (inside
// the machine on a Mac or Windows), or "" when Podman can't say.
func podmanSocket(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := engineCmd(ctx, enginePodman, "info", "--format", "{{.Host.RemoteSocket.Path}}").Output()
	if err != nil {
		return ""
	}
	p := strings.TrimPrefix(strings.TrimSpace(string(out)), "unix://")
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	return p
}
