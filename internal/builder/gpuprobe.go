package builder

// GPU access check (POST /api/gpu/probe): can containers on each engine reach a
// GPU? It's user-triggered because it starts a throwaway container per engine.
// Each engine's answer is saved (gpustate.go): an engine whose containers got
// a usable render node gives Vulkan from then on (engineGPU), until its
// machine changes or a Vulkan run there finds no /dev/dri.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
	// RenderAccess is who could open the render node (renderD*) the container
	// got: "all", "group" (members of RenderGID), "root", "none" (no render
	// node arrived) or "" (couldn't tell). The factory's Vulkan images run as an
	// unprivileged user, so "group" needs --group-add and "root" can't work.
	RenderAccess string   `json:"render_access,omitempty"`
	RenderGID    int      `json:"render_gid,omitempty"`
	CDIDevices   []string `json:"cdi_devices,omitempty"` // docker info "Discovered Devices" (CDI)
	WebGPU       bool     `json:"webgpu"`                // Docker Desktop's experimental docker.com/gpu=webgpu
	Verdict      string   `json:"verdict"`
}

// usableRender reports whether the factory's Vulkan images (an unprivileged
// user) can open the render node the probe saw: anyone can, its group can (Run
// adds the group), or the listing didn't say (older results).
func (p GPUProbe) usableRender() bool {
	return p.DRI && p.RenderAccess != "root" && p.RenderAccess != "none"
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

// gpuProbeArgs is the probe container: hand it /dev/dri and list what arrives,
// with permissions and numeric owners (the device nodes keep the VM's, which
// decide whether the images' unprivileged user can open them). Podman gets the
// same security opts Run gives Vulkan models (the remote seccomp-path mismatch,
// and SELinux on the libkrun machine blocks /dev/dri); Docker needs neither.
func gpuProbeArgs(engine, image string) []string {
	args := []string{"run", "--rm", "--network", "none", "--label", "local-llm.tool=gpu-probe", "--device", "/dev/dri"}
	if resolveEngine(engine) == enginePodman {
		args = append(args, "--security-opt", "seccomp=unconfined", "--security-opt", "label=disable")
	}
	return append(args, image, "ls", "-ln", "/dev/dri")
}

// driEntry is one line of the probe's /dev/dri listing.
type driEntry struct {
	Name string
	Mode string // "crw-rw-rw-", "" from a short listing
	GID  int    // -1 when unknown
}

// parseDRIList reads the probe's listing of /dev/dri: `ls -ln` lines
// ("crw-rw----    1 0   44   226,   0 Nov 15 10:00 card0", busybox or GNU),
// or one name per line (`ls -1`, older probes). Skips "total N".
func parseDRIList(out string) []driEntry {
	var devs []driEntry
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		switch {
		case len(f) == 1:
			devs = append(devs, driEntry{Name: f[0], GID: -1})
		case len(f) >= 8 && len(f[0]) >= 10 && strings.ContainsRune("-bcdlps", rune(f[0][0])):
			name := f[len(f)-1]
			if f[0][0] == 'l' && len(f) >= 3 && f[len(f)-2] == "->" {
				name = f[len(f)-3]
			}
			gid, err := strconv.Atoi(f[3])
			if err != nil {
				gid = -1
			}
			devs = append(devs, driEntry{Name: name, Mode: f[0][:10], GID: gid})
		}
	}
	return devs
}

// driNames lists the entries' names.
func driNames(devs []driEntry) []string {
	names := make([]string, 0, len(devs))
	for _, d := range devs {
		names = append(names, d.Name)
	}
	return names
}

// renderAccess says who can open the first render node (renderD*) in the
// listing, from its mode: "all" (others may read and write), "group" (its
// group may: gid), "root", "none" (no render node), or "" (no modes listed).
func renderAccess(devs []driEntry) (access string, gid int) {
	for _, d := range devs {
		if !strings.HasPrefix(d.Name, "renderD") {
			continue
		}
		if len(d.Mode) < 10 {
			return "", 0
		}
		switch {
		case d.Mode[7] == 'r' && d.Mode[8] == 'w':
			return "all", 0
		case d.Mode[4] == 'r' && d.Mode[5] == 'w' && d.GID >= 0:
			return "group", d.GID
		default:
			return "root", 0
		}
	}
	return "none", 0
}

// engineInfo is what `<engine> info --format '{{json .}}'` says about the
// machine behind an engine.
type engineInfo struct {
	MemBytes int64
	CPUs     int
	Identity EngineIdentity
	CDI      []string
	Labels   []string // docker's daemon labels
}

// parseEngineInfo reads `<engine> info --format '{{json .}}'`. Docker reports
// MemTotal, NCPU, OperatingSystem, KernelVersion, ID, Name, Architecture and
// DiscoveredDevices ([{"Source":"cdi","ID":"docker.com/gpu=webgpu"}]); podman
// reports them under host (memTotal, cpus, distribution, kernel, hostname, arch).
func parseEngineInfo(out []byte) (engineInfo, bool) {
	var doc struct {
		MemTotal          int64
		NCPU              int
		OperatingSystem   string
		KernelVersion     string
		ID                string
		Name              string
		Architecture      string
		Labels            []string
		DiscoveredDevices []struct{ Source, ID string }
		Host              *struct {
			MemTotal     int64  `json:"memTotal"`
			CPUs         int    `json:"cpus"`
			Kernel       string `json:"kernel"`
			Hostname     string `json:"hostname"`
			Arch         string `json:"arch"`
			Distribution struct {
				Distribution string `json:"distribution"`
				Version      string `json:"version"`
			} `json:"distribution"`
		} `json:"host"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return engineInfo{}, false
	}
	var in engineInfo
	switch {
	case doc.Host != nil:
		h := doc.Host
		in.MemBytes, in.CPUs = h.MemTotal, h.CPUs
		in.Identity = EngineIdentity{
			System: strings.TrimSpace(h.Distribution.Distribution + " " + h.Distribution.Version),
			Kernel: h.Kernel, Name: h.Hostname, Arch: h.Arch,
		}
	default:
		in.MemBytes, in.CPUs = doc.MemTotal, doc.NCPU
		in.Identity = EngineIdentity{
			System: doc.OperatingSystem, Kernel: doc.KernelVersion,
			ID: doc.ID, Name: doc.Name, Arch: doc.Architecture,
		}
		in.Labels = doc.Labels
	}
	for _, d := range doc.DiscoveredDevices {
		if d.ID != "" {
			in.CDI = append(in.CDI, d.ID)
		}
	}
	return in, true
}

// readEngineInfo asks an engine for its info as JSON (one call: memory, CPUs
// and the machine's identity).
func readEngineInfo(ctx context.Context, engine string) (engineInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := engineCmd(ctx, engine, "info", "--format", "{{json .}}").Output()
	if err != nil {
		return engineInfo{}, err
	}
	in, ok := parseEngineInfo(out)
	if !ok {
		return engineInfo{}, fmt.Errorf("%s info: unexpected output %q", resolveEngine(engine), firstLine(string(out)))
	}
	return in, nil
}

// firstLine is s's first line, trimmed and cut to 200 bytes, for error text.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// parseEngineSystem pulls a one-line system description and the CDI devices out
// of `<engine> info --format '{{json .}}'` (see parseEngineInfo). Plain-text
// `docker info` output is accepted too (its "Discovered Devices:" section).
func parseEngineSystem(out []byte) (system string, cdi []string) {
	in, ok := parseEngineInfo(out)
	if !ok {
		return "", parseDiscoveredDevicesText(string(out))
	}
	return in.Identity.describe(), in.CDI
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

// verdictHost is what gpuVerdict needs to know about the computer: its GPU
// path from the start scripts (FACTORY_GPU) and its OS (HostOS).
type verdictHost struct {
	FactoryGPU string
	OS         string
}

// gpuVerdict sums up a probe in a sentence or two for the System page.
func gpuVerdict(p GPUProbe, h verdictHost) string {
	docker := p.Engine == engineDocker
	mac := h.OS == "darwin"
	otherGPU := "Run GPU images on another engine."
	if docker && mac {
		otherGPU = "Run GPU images on Podman (libkrun) instead."
	}
	switch {
	case !p.Reachable:
		return "Not reachable — start it (then restart the factory so it reconnects) and check again."
	case p.DRI && p.RenderAccess == "none":
		return "A GPU device (/dev/dri) reached the container, but no render node (renderD128) came with it, and Vulkan needs one, so containers here run on the CPU. " + otherGPU
	case p.DRI && p.RenderAccess == "root":
		return "A GPU device (/dev/dri) reached the container, but only root can open its render node, and the factory's Vulkan images run as an unprivileged user, so they'd fall back to the CPU. " + otherGPU
	case p.DRI && h.FactoryGPU == "cuda":
		return "A GPU device (/dev/dri) reached the container. This machine's GPU path is CUDA, so the factory doesn't build Vulkan images for it."
	case p.DRI && docker:
		where := ""
		if mac {
			where = " On a Mac that's Docker VMM or a krunkit VM."
		}
		return "A GPU device (/dev/dri) reached the container, so the factory now builds and runs Vulkan images on Docker too." + where +
			" Experimental: this path is unverified with the factory's images. Check again after changing Docker's VM."
	case p.DRI:
		return "A GPU device (/dev/dri) reached the container — Vulkan images can use the GPU on this engine."
	case p.WebGPU:
		return "No /dev/dri, so Vulkan images can't reach the GPU here. Docker Desktop does offer an experimental WebGPU device (" + webGPUDevice + "), but llama.cpp's WebGPU backend needs Dawn inside the container, so the factory can't use it yet. Run GPU images on Podman (libkrun), or switch Docker Desktop to Docker VMM (Settings → General → Virtual Machine Manager) and check again (experimental)."
	case p.Engine == enginePodman:
		return "No GPU device — this Podman machine isn't a libkrun (krunkit) one, so containers run on the CPU."
	case docker && mac:
		return "No GPU device — Docker's VM doesn't pass one through (Docker Desktop's default, Apple Virtualization, has none), so containers here run on the CPU. To try Docker's GPU, switch Docker Desktop to Docker VMM (Settings → General → Virtual Machine Manager), then check again (experimental)."
	default:
		return "No GPU device — containers on this engine run on the CPU."
	}
}

// ProbeGPU checks every installed engine concurrently: its `info` (system and
// CDI devices) and a throwaway container given --device /dev/dri. Each
// reachable engine's answer is saved with the machine's identity; an engine
// that didn't answer keeps its last saved result.
func (b *Builder) ProbeGPU(ctx context.Context) []GPUProbe {
	engines := candidateEngines()
	out := make([]GPUProbe, len(engines))
	ids := make([]EngineIdentity, len(engines))
	h := verdictHost{FactoryGPU: os.Getenv("FACTORY_GPU"), OS: HostOS()}
	var wg sync.WaitGroup
	for i, e := range engines {
		wg.Add(1)
		go func(i int, e string) {
			defer wg.Done()
			out[i], ids[i] = probeEngineGPU(ctx, e, h)
		}(i, e)
	}
	wg.Wait()
	now := time.Now()
	for i, p := range out {
		if !p.Reachable {
			continue
		}
		if err := b.saveGPUCheck(p.Engine, GPUCheckRecord{CheckedAt: now, Source: gpuCheckProbe, Identity: ids[i], Probe: p}); err != nil {
			out[i].Verdict += " (Couldn't save this result: " + err.Error() + ")"
		}
	}
	return out
}

func probeEngineGPU(ctx context.Context, engine string, h verdictHost) (GPUProbe, EngineIdentity) {
	p := GPUProbe{Engine: engine}
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	info, err := engineCmd(ictx, engine, "info", "--format", "{{json .}}").Output()
	cancel()
	if err != nil {
		p.Verdict = gpuVerdict(p, h)
		return p, EngineIdentity{}
	}
	p.Reachable = true
	in, _ := parseEngineInfo(info)
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
		devs := parseDRIList(string(stdout))
		p.DRIDevices = driNames(devs)
		p.DRI = len(devs) > 0
		if p.DRI {
			p.RenderAccess, p.RenderGID = renderAccess(devs)
		} else {
			p.DRIError = "/dev/dri is empty in the container"
		}
	}
	p.Verdict = gpuVerdict(p, h)
	return p, in.Identity
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
