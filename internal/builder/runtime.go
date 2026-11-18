package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"
)

// A model-less runtime is the llama.cpp chat image without weights: the
// Dockerfile's runtime-base target, which holds llama-server, llmgate, the
// environment and the entrypoint. The weights are mounted when it runs:
//
//	podman run --rm --device /dev/dri -p 127.0.0.1:8080:8080 \
//	  -v <models dir>:/models:ro -e MODEL_PATH=/models/<file>.gguf \
//	  local-llm/runtime:vulkan-v0.9.0-arm64
//
// It serves the model as it is: no system prompt and no harness unless they
// are mounted and named with SYSTEM_PROMPT_FILE and HARNESS_FILE, and a vision
// projector only when MMPROJ_PATH names one. FileFreak's CLI runs its own
// agent against it with "harness": false.

// RuntimeTarget is the Dockerfile stage of the model-less runtime.
const RuntimeTarget = "runtime-base"

// DefaultRuntimeImage is the repository a model-less runtime is tagged in.
const DefaultRuntimeImage = "local-llm/runtime"

// Platforms an image can be built for. "" is the engine's own.
var platforms = map[string]string{"linux/amd64": "amd64", "linux/arm64": "arm64"}

// RuntimeOptions configures a model-less llama.cpp runtime image.
type RuntimeOptions struct {
	Engine    string // "docker" (default) | "podman"
	Compute   string // "cpu" | "cuda" | "vulkan"
	Platform  string // "" (the engine's own), "linux/amd64" or "linux/arm64"
	ImageName string // defaults to DefaultRuntimeImage
	Tag       string // defaults to <compute>-<llama.cpp ref>-<arch>
	ExportTar bool   // also `<engine> save` into ./images
}

// PlatformArch checks a build platform and returns its architecture: the
// platform's, or this computer's for "" (an engine builds for its own).
func PlatformArch(platform string) (string, error) {
	if platform == "" {
		return goruntime.GOARCH, nil
	}
	arch, ok := platforms[platform]
	if !ok {
		return "", fmt.Errorf("unknown platform %q (want linux/amd64 or linux/arm64)", platform)
	}
	return arch, nil
}

var llamaRefRe = regexp.MustCompile(`(?m)^ARG LLAMA_CPP_REF=(\S+)`)

// llamaCppRef is the llama.cpp release a Dockerfile compiles.
func llamaCppRef(dockerfile []byte) string {
	if m := llamaRefRe.FindSubmatch(dockerfile); m != nil {
		return string(m[1])
	}
	return "unknown"
}

// ref is the image reference of a model-less runtime.
func (o RuntimeOptions) ref(llamaRef, arch string) string {
	name := strings.TrimSpace(o.ImageName)
	if name == "" {
		name = DefaultRuntimeImage
	}
	tag := strings.TrimSpace(o.Tag)
	if tag == "" {
		tag = fmt.Sprintf("%s-%s-%s", o.Compute, llamaRef, arch)
	}
	return name + ":" + tag
}

// runtimeBuildArgs is the `<engine> build` command line of a model-less runtime.
func runtimeBuildArgs(ref, compute, llamaRef, arch, platform, engine, work string) []string {
	args := []string{
		"build", "-t", ref, "--target", RuntimeTarget,
		"--label", "local-llm.tool=runtime",
		"--label", "local-llm.compute=" + compute,
		"--label", "local-llm.llama_cpp_ref=" + llamaRef,
		"--label", "local-llm.arch=" + arch,
		"--label", "local-llm.engine=" + engine,
	}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	if engine == enginePodman {
		args = append(args, "--security-opt", "seccomp=unconfined") // see Build
	}
	return append(args, work)
}

type resolvedRuntime struct {
	opts                RuntimeOptions
	arch, llamaRef, ref string
	dockerfile          []byte
}

// resolveRuntime checks the options and reads the Dockerfile they build.
func (b *Builder) resolveRuntime(o RuntimeOptions) (resolvedRuntime, error) {
	if o.Compute == "" {
		o.Compute = "cpu"
	}
	if o.Compute != "cpu" && o.Compute != "cuda" && o.Compute != "vulkan" {
		return resolvedRuntime{}, fmt.Errorf("unknown compute %q (want cpu, cuda, or vulkan)", o.Compute)
	}
	arch, err := PlatformArch(o.Platform)
	if err != nil {
		return resolvedRuntime{}, err
	}
	df, err := os.ReadFile(b.runtimeDockerfile(dockerfileName("llama", o.Compute)))
	if err != nil {
		return resolvedRuntime{}, err
	}
	if !strings.Contains(string(df), " AS "+RuntimeTarget+"\n") {
		return resolvedRuntime{}, fmt.Errorf("Dockerfile.%s has no %s stage", o.Compute, RuntimeTarget)
	}
	llamaRef := llamaCppRef(df)
	return resolvedRuntime{opts: o, arch: arch, llamaRef: llamaRef, ref: o.ref(llamaRef, arch), dockerfile: df}, nil
}

// RuntimeRef checks the options and returns the reference BuildRuntime tags.
func (b *Builder) RuntimeRef(o RuntimeOptions) (string, error) {
	r, err := b.resolveRuntime(o)
	return r.ref, err
}

// BuildRuntime builds the model-less llama.cpp runtime for one compute target
// and platform, and returns its reference.
func (b *Builder) BuildRuntime(ctx context.Context, o RuntimeOptions, log LogFunc) (string, error) {
	r, err := b.resolveRuntime(o)
	if err != nil {
		return "", err
	}
	o, arch, llamaRef, ref, df := r.opts, r.arch, r.llamaRef, r.ref, r.dockerfile
	engine := resolveEngine(o.Engine)

	plan, err := b.planBuild(ctx, engine, o.Compute, log)
	if err != nil {
		return "", err
	}
	work := filepath.Join(b.BuildDir, fmt.Sprintf("ctx-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	if err := os.WriteFile(filepath.Join(work, "Dockerfile"), df, 0o644); err != nil {
		return "", err
	}
	if err := stageGate(b.BaseDir, work); err != nil {
		return "", err
	}

	if arch != goruntime.GOARCH {
		log(fmt.Sprintf("Building for %s on an %s computer: the engine emulates %s, so compiling llama.cpp is much slower (docs/RUNTIME.md).", o.Platform, goruntime.GOARCH, arch))
	}
	log(fmt.Sprintf("Building the model-less runtime %s [%s, llama.cpp %s, %s] on %s ...", ref, o.Compute, llamaRef, arch, engineLabel(plan.Build)))
	release, err := b.acquireBuildSlot(ctx, plan.Build, log)
	if err != nil {
		return "", err
	}
	tail, err := runStreamingTail(ctx, b.BaseDir, plan.Build, log, 200, runtimeBuildArgs(ref, o.Compute, llamaRef, arch, o.Platform, plan.Target, work)...)
	release()
	if err != nil {
		err = fmt.Errorf("%s build failed: %w", plan.Build, err)
		if ctx.Err() == nil && compilerOOM(tail) {
			return "", &HintError{Hint: oomHint(plan.Build, plan.BuildGB), Err: err}
		}
		return "", err
	}
	log("Built runtime: " + ref)

	var tarPath string
	if o.ExportTar {
		tarPath = filepath.Join(b.ImagesDir, exportFileName(ref))
		log("Exporting to images/" + filepath.Base(tarPath) + " ...")
		if err := runStreaming(ctx, b.BaseDir, plan.Build, log, "save", "-o", tarPath, ref); err != nil {
			return "", fmt.Errorf("%s save failed: %w", plan.Build, err)
		}
	}
	if plan.Transfer {
		if err := b.transferImage(ctx, plan.Build, plan.Target, ref, tarPath, log); err != nil {
			return "", err
		}
	}
	return ref, nil
}
