package builder

// GPU access check (POST /api/gpu/probe): can containers on each engine reach a
// GPU? It's user-triggered because it starts a throwaway container per engine.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// GPUProbe is one engine's answer.
type GPUProbe struct {
	Engine     string   `json:"engine"`
	Reachable  bool     `json:"reachable"`
	System     string   `json:"system,omitempty"`      // e.g. "Docker Desktop · kernel 7.0.14-linuxkit"
	DRI        bool     `json:"dri"`                   // a probe container got the GPU render node
	DRIDevices []string `json:"dri_devices,omitempty"` // what it saw in /dev/dri (card0, renderD128, …)
	DRIError   string   `json:"dri_error,omitempty"`   // why it couldn't, from the engine
	CDIDevices []string `json:"cdi_devices,omitempty"` // docker info "Discovered Devices" (CDI)
	WebGPU     bool     `json:"webgpu"`                // Docker Desktop's experimental docker.com/gpu=webgpu
	Verdict    string   `json:"verdict"`
}

// webGPUDevice is the CDI device Docker Desktop on macOS exposes: WebGPU API
// remoting to the host (Dawn on Metal). Undocumented and experimental, and
// llama.cpp's WebGPU backend needs Dawn inside the container, so the factory
// can't use it yet.
const webGPUDevice = "docker.com/gpu=webgpu"

// gpuProbeImage is the throwaway image the probe runs: tiny, and named in full
// so podman's short-name resolution never has to prompt.
func gpuProbeImage() string {
	if img := os.Getenv("GPU_PROBE_IMAGE"); img != "" {
		return img
	}
	return "docker.io/library/alpine:latest"
}

// gpuProbeArgs is the probe container: hand it /dev/dri and list what arrives.
// Podman gets the same security opts Run gives Vulkan models (the remote
// seccomp-path mismatch, and SELinux on the libkrun machine blocks /dev/dri).
func gpuProbeArgs(engine, image string) []string {
	args := []string{"run", "--rm", "--network", "none", "--label", "local-llm.tool=gpu-probe", "--device", "/dev/dri"}
	if resolveEngine(engine) == enginePodman {
		args = append(args, "--security-opt", "seccomp=unconfined", "--security-opt", "label=disable")
	}
	return append(args, image, "ls", "-1", "/dev/dri")
}

// parseDRIList reads `ls -1 /dev/dri` output: one device per line.
func parseDRIList(out string) []string {
	var devs []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 1 {
			devs = append(devs, f[0])
		}
	}
	return devs
}

// parseEngineSystem pulls a one-line system description and the CDI devices out
// of `<engine> info --format '{{json .}}'`. Docker reports OperatingSystem,
// KernelVersion and DiscoveredDevices ([{"Source":"cdi","ID":"docker.com/gpu=webgpu"}]);
// podman reports host.distribution and host.kernel. Plain-text `docker info`
// output is accepted too (its "Discovered Devices:" section).
func parseEngineSystem(out []byte) (system string, cdi []string) {
	var doc struct {
		OperatingSystem   string
		KernelVersion     string
		DiscoveredDevices []struct{ Source, ID string }
		Host              *struct {
			Kernel       string `json:"kernel"`
			Distribution struct {
				Distribution string `json:"distribution"`
				Version      string `json:"version"`
			} `json:"distribution"`
		} `json:"host"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return "", parseDiscoveredDevicesText(string(out))
	}
	var parts []string
	switch {
	case doc.OperatingSystem != "":
		parts = append(parts, doc.OperatingSystem)
		if doc.KernelVersion != "" {
			parts = append(parts, "kernel "+doc.KernelVersion)
		}
	case doc.Host != nil:
		if d := strings.TrimSpace(doc.Host.Distribution.Distribution + " " + doc.Host.Distribution.Version); d != "" {
			parts = append(parts, d)
		}
		if doc.Host.Kernel != "" {
			parts = append(parts, "kernel "+doc.Host.Kernel)
		}
	}
	for _, d := range doc.DiscoveredDevices {
		if d.ID != "" {
			cdi = append(cdi, d.ID)
		}
	}
	return strings.Join(parts, " · "), cdi
}

// parseDiscoveredDevicesText reads the device IDs under plain `docker info`'s
//
//	Discovered Devices:
//	 cdi: docker.com/gpu=webgpu
func parseDiscoveredDevicesText(out string) []string {
	var ids []string
	in := false
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		if t == "Discovered Devices:" {
			in = true
			continue
		}
		if !in {
			continue
		}
		// The section ends at the next line that isn't indented deeper than its header.
		if !strings.HasPrefix(line, "  ") || t == "" {
			break
		}
		if i := strings.Index(t, ":"); i >= 0 {
			if id := strings.TrimSpace(t[i+1:]); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// gpuVerdict sums up a probe in a sentence for the Settings page.
func gpuVerdict(p GPUProbe) string {
	switch {
	case !p.Reachable:
		return "Not reachable — start it (then restart the factory so it reconnects) and check again."
	case p.DRI && p.Engine == engineDocker:
		return "A GPU device (/dev/dri) reached the container. On a Mac that means Docker VMM's virtio-gpu — experimental: Vulkan images may work here, but this path is unverified with the factory's image."
	case p.DRI:
		return "A GPU device (/dev/dri) reached the container — Vulkan images can use the GPU on this engine."
	case p.WebGPU:
		return "No /dev/dri, so Vulkan images can't reach the GPU here. Docker Desktop does offer an experimental WebGPU device (" + webGPUDevice + "), but llama.cpp's WebGPU backend needs Dawn inside the container, so the factory can't use it yet. Run GPU images on Podman (libkrun), or try switching Docker Desktop to Docker VMM (unverified)."
	case p.Engine == enginePodman:
		return "No GPU device — this Podman machine isn't a libkrun (krunkit) one, so containers run on the CPU."
	default:
		return "No GPU device — containers on this engine run on the CPU."
	}
}

// ProbeGPU checks every installed engine concurrently: its `info` (system and
// CDI devices) and a throwaway container given --device /dev/dri.
func (b *Builder) ProbeGPU(ctx context.Context) []GPUProbe {
	engines := candidateEngines()
	out := make([]GPUProbe, len(engines))
	var wg sync.WaitGroup
	for i, e := range engines {
		wg.Add(1)
		go func(i int, e string) {
			defer wg.Done()
			out[i] = probeEngineGPU(ctx, e)
		}(i, e)
	}
	wg.Wait()
	return out
}

func probeEngineGPU(ctx context.Context, engine string) GPUProbe {
	p := GPUProbe{Engine: engine}
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	info, err := engineCmd(ictx, engine, "info", "--format", "{{json .}}").Output()
	cancel()
	if err != nil {
		p.Verdict = gpuVerdict(p)
		return p
	}
	p.Reachable = true
	p.System, p.CDIDevices = parseEngineSystem(info)
	for _, d := range p.CDIDevices {
		p.WebGPU = p.WebGPU || d == webGPUDevice
	}
	// Generous timeout: the first probe may pull the (tiny) image.
	rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	stdout, err := engineCmd(rctx, engine, gpuProbeArgs(engine, gpuProbeImage())...).Output()
	switch {
	case err != nil && rctx.Err() != nil:
		p.DRIError = "the probe container didn't finish within 90s"
	case err != nil:
		p.DRIError = probeErrText(err)
	default:
		p.DRIDevices = parseDRIList(string(stdout))
		p.DRI = len(p.DRIDevices) > 0
		if !p.DRI {
			p.DRIError = "/dev/dri is empty in the container"
		}
	}
	p.Verdict = gpuVerdict(p)
	return p
}

// probeErrText is the engine's own complaint (the last stderr line), else err.
func probeErrText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		lines := strings.Split(strings.TrimSpace(string(ee.Stderr)), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return last
		}
	}
	return fmt.Sprint(err)
}
