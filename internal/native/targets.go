// Package native describes native packages: a model and its agent harness in
// one download per operating system, run by the llmpack launcher with
// llama.cpp's prebuilt binaries instead of a container (docs/NATIVE.md).
//
// It holds what both sides need: the targets the factory can package for
// (with the pinned llama.cpp release for each), the manifest the factory
// writes and the launcher reads, and the launcher's process supervisor.
// Standard library only: the launcher is cross-compiled with CGO_ENABLED=0.
package native

import "sort"

// The llama.cpp release native packages carry. The model images build
// llama.cpp from source at LLAMA_CPP_REF=v0.9.0 (scripts/docker/runtimes/Dockerfile.cpu); that
// tag and the prebuilt release b12110 point at the same commit, so a native
// package runs the same llama.cpp as a container built by this factory.
// Moving the images to another release means moving these too: the new
// build's tag, commit and every asset's SHA-256 (GitHub lists each asset's
// digest; the release's nightly-tag.txt names the build of a vX.Y.Z tag).
const (
	LlamaCppRelease = "v0.9.0"
	LlamaCppBuild   = "b12110"
	LlamaCppCommit  = "cd959bae99ff4bed22bef2604f45241c09bbb8c8"
	llamaCppBase    = "https://github.com/ggml-org/llama.cpp/releases/download/" + LlamaCppBuild + "/"
)

// GPU acceleration a target's llama.cpp build uses.
const (
	AccelMetal  = "metal"  // Apple Silicon GPU
	AccelVulkan = "vulkan" // any Vulkan GPU driver; falls back to the CPU without one
	AccelCPU    = "cpu"
)

// Target is an operating system and CPU a package can be made for.
type Target struct {
	ID     string `json:"id"`    // e.g. "macos-arm64"; also the package file name suffix
	Label  string `json:"label"` // e.g. "macOS (Apple Silicon)"
	OS     string `json:"os"`    // macos | linux | windows
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	Accel  string `json:"accel"`
	// GPULayers is the launcher's default --n-gpu-layers: "all" on Apple
	// Silicon (one memory pool, the GPU can take every layer), "auto" for a
	// discrete GPU (llama.cpp fits as many layers as its memory holds).
	GPULayers string `json:"gpu_layers"`
	// Requirements is what the computer needs, in a sentence for people.
	Requirements string `json:"requirements"`
	// Preview marks a target packaged the same way as macOS but not yet run
	// on a machine of its kind; the UI says so.
	Preview bool `json:"preview"`

	// Asset is the llama.cpp release file (in LlamaCppBuild) and its SHA-256.
	Asset       string `json:"asset"`
	AssetSHA256 string `json:"asset_sha256"`
}

// AssetURL is where the target's llama.cpp archive is downloaded from.
func (t Target) AssetURL() string { return llamaCppBase + t.Asset }

// Exe is a program's file name on the target ("llama-server.exe" on Windows).
func (t Target) Exe(name string) string {
	if t.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// ServerName is llama-server's file name on the target.
func (t Target) ServerName() string { return t.Exe("llama-server") }

// LauncherName is the launcher's file name on the target.
func (t Target) LauncherName() string { return t.Exe("llmpack") }

// targets are the operating systems native packages can be made for. macOS
// on Apple Silicon is the one built for first and tried; Linux and Windows use
// llama.cpp's Vulkan builds, which also run on the CPU alone.
var targets = []Target{
	{
		ID: "macos-arm64", Label: "macOS (Apple Silicon)", OS: "macos", GOOS: "darwin", GOARCH: "arm64",
		Accel: AccelMetal, GPULayers: "all",
		Requirements: "A Mac with an Apple M-series chip (M1 or later) and macOS 13.3 or later.",
		Asset:        "llama-" + LlamaCppBuild + "-bin-macos-arm64.tar.gz",
		AssetSHA256:  "4f5b8de12e0b9516658ca6a5764c6ffb3d7c659c3dd538556b1413c7b4050662",
	},
	{
		ID: "linux-x64", Label: "Linux (x86-64)", OS: "linux", GOOS: "linux", GOARCH: "amd64",
		Accel: AccelVulkan, GPULayers: "auto", Preview: true,
		Requirements: "A 64-bit Intel or AMD PC with glibc 2.34 or later (Ubuntu 22.04, Debian 12, Fedora 35 or newer). A GPU with a Vulkan driver is used when present; otherwise the model runs on the CPU.",
		Asset:        "llama-" + LlamaCppBuild + "-bin-ubuntu-vulkan-x64.tar.gz",
		AssetSHA256:  "a5d741940956ec62e8783aee7e5a4c7c761af867d6f027c3277a51e1a3ac59b0",
	},
	{
		ID: "windows-x64", Label: "Windows (x86-64)", OS: "windows", GOOS: "windows", GOARCH: "amd64",
		Accel: AccelVulkan, GPULayers: "auto", Preview: true,
		Requirements: "64-bit Windows 10 or 11 on an Intel or AMD PC. A GPU whose driver supports Vulkan (NVIDIA, AMD and Intel drivers do) is used when present; otherwise the model runs on the CPU.",
		Asset:        "llama-" + LlamaCppBuild + "-bin-win-vulkan-x64.zip",
		AssetSHA256:  "9732a5e48887ca54d0bfe4af4324dc85fd56cd8cda19b52df3542fa6ebe6874f",
	},
}

// Targets lists the targets, macOS first.
func Targets() []Target {
	out := append([]Target(nil), targets...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].OS == "macos" && out[j].OS != "macos" })
	return out
}

// TargetByID finds a target.
func TargetByID(id string) (Target, bool) {
	for _, t := range targets {
		if t.ID == id {
			return t, true
		}
	}
	return Target{}, false
}
