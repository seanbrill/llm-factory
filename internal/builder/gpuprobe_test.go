package builder

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestGPUProbeArgs(t *testing.T) {
	img := "docker.io/library/alpine:latest"
	docker := gpuProbeArgs("docker", img)
	want := []string{"run", "--rm", "--network", "none", "--label", "local-llm.tool=gpu-probe", "--device", "/dev/dri", img, "ls", "-ln", "/dev/dri"}
	if !slices.Equal(docker, want) {
		t.Errorf("docker args = %q\nwant %q", docker, want)
	}
	podman := gpuProbeArgs("podman", img)
	wantP := []string{"run", "--rm", "--network", "none", "--label", "local-llm.tool=gpu-probe", "--device", "/dev/dri",
		"--security-opt", "seccomp=unconfined", "--security-opt", "label=disable", img, "ls", "-ln", "/dev/dri"}
	if !slices.Equal(podman, wantP) {
		t.Errorf("podman args = %q\nwant %q", podman, wantP)
	}
	// The probe is a throwaway listing: never detached, never kept, no network.
	for _, args := range [][]string{docker, podman} {
		if slices.Contains(args, "-d") || !slices.Contains(args, "--rm") {
			t.Errorf("probe must be --rm and attached: %q", args)
		}
	}
}

func TestGPUProbeImage(t *testing.T) {
	t.Setenv("GPU_PROBE_IMAGE", "")
	if got := gpuProbeImage(); got != "docker.io/library/alpine:latest" {
		t.Errorf("default image = %q (must be fully qualified for podman)", got)
	}
	t.Setenv("GPU_PROBE_IMAGE", "registry.local/busybox:1")
	if got := gpuProbeImage(); got != "registry.local/busybox:1" {
		t.Errorf("override = %q", got)
	}
}

// busybox `ls -ln /dev/dri` in a container given --device /dev/dri: the nodes
// keep the VM's owner and mode.
const (
	lsKrun = "total 0\n" +
		"drwxr-xr-x    2 0        0               80 Nov 15 10:00 by-path\n" +
		"crw-rw----    1 0        39       226,   0 Nov 15 10:00 card0\n" +
		"crw-rw-rw-    1 0        105      226, 128 Nov 15 10:00 renderD128\n"
	lsGroup = "total 0\n" +
		"crw-rw----    1 0        44       226,   0 Nov 15 10:00 card0\n" +
		"crw-rw----    1 0        993      226, 128 Nov 15 10:00 renderD128\n"
	lsRoot = "crw-------    1 0        0        226,   0 Jan  1 00:00 card0\n" +
		"crw-------    1 0        0        226, 128 Jan  1 00:00 renderD128\n"
	// GNU ls, with an ACL mark and a symlink.
	lsGNU = "total 0\n" +
		"lrwxrwxrwx  1 0   0       8 Nov 15 10:00 render -> renderD128\n" +
		"crw-rw-rw-+ 1 0 105 226, 128 Nov 15 10:00 renderD128\n"
	lsCardOnly = "crw-rw-rw-    1 0        0        226,   0 Nov 15 10:00 card0\n"
)

func TestParseDRIList(t *testing.T) {
	devs := parseDRIList(lsKrun)
	if got := driNames(devs); !slices.Equal(got, []string{"by-path", "card0", "renderD128"}) {
		t.Errorf("names = %q", got)
	}
	if devs[2].Mode != "crw-rw-rw-" || devs[2].GID != 105 {
		t.Errorf("renderD128 = %+v", devs[2])
	}
	if got := driNames(parseDRIList(lsGNU)); !slices.Equal(got, []string{"render", "renderD128"}) {
		t.Errorf("GNU names = %q", got)
	}
	// Older probes listed names only.
	if got := driNames(parseDRIList("by-path\ncard0\nrenderD128\n")); !slices.Equal(got, []string{"by-path", "card0", "renderD128"}) {
		t.Errorf("short listing = %q", got)
	}
	if got := parseDRIList(""); len(got) != 0 {
		t.Errorf("empty listing = %+v", got)
	}
	if got := driNames(parseDRIList("  \nrenderD128\r\n")); !slices.Equal(got, []string{"renderD128"}) {
		t.Errorf("blank lines/CRLF = %q", got)
	}
}

func TestRenderAccess(t *testing.T) {
	cases := []struct {
		name, listing string
		access        string
		gid           int
	}{
		{"libkrun / udev: everyone", lsKrun, "all", 0},
		{"render group only", lsGroup, "group", 993},
		{"no udev: root only", lsRoot, "root", 0},
		{"GNU ls with ACL", lsGNU, "all", 0},
		{"no render node", lsCardOnly, "none", 0},
		{"names only: can't tell", "card0\nrenderD128\n", "", 0},
	}
	for _, c := range cases {
		access, gid := renderAccess(parseDRIList(c.listing))
		if access != c.access || gid != c.gid {
			t.Errorf("%s: renderAccess = %q, %d; want %q, %d", c.name, access, gid, c.access, c.gid)
		}
	}
}

// dockerInfoMac is trimmed `docker info --format '{{json .}}'` from Docker
// Desktop 4.96 on an M4 Pro (Apple Virtualization).
const dockerInfoMac = `{"ID":"b7d1c5a2","Name":"docker-desktop","Containers":3,"Driver":"overlayfs","NCPU":14,"MemTotal":18829856768,
"OperatingSystem":"Docker Desktop","OSType":"linux","Architecture":"aarch64","KernelVersion":"7.0.14-linuxkit",
"CDISpecDirs":["/etc/cdi","/var/run/cdi"],"DiscoveredDevices":[{"Source":"cdi","ID":"docker.com/gpu=webgpu"}],
"Labels":["com.docker.desktop.address=unix:///Users/me/Library/Containers/com.docker.docker/Data/docker-cli.sock"]}`

// dockerInfoLima is the same from Docker Engine in a Lima VM (illustrative:
// Lima's default Ubuntu template).
const dockerInfoLima = `{"ID":"0f6e2b9c-6c51-4a8e-9a43-0c2b5d0e7f11","Name":"lima-docker","NCPU":8,"MemTotal":12541575168,
"OperatingSystem":"Ubuntu 25.10","OSType":"linux","Architecture":"aarch64","KernelVersion":"6.17.0-6-generic","Labels":[]}`

// podmanInfoKrun is trimmed `podman info --format '{{json .}}'` from a libkrun
// Podman machine.
const podmanInfoKrun = `{"host":{"arch":"arm64","cpus":7,"memTotal":2006432768,"hostname":"localhost.localdomain",
"distribution":{"distribution":"fedora","variant":"coreos","version":"43"},"kernel":"6.17.7-300.fc43.aarch64",
"os":"linux","remoteSocket":{"path":"/run/podman/podman.sock","exists":true}},"store":{"graphDriverName":"overlay"},
"version":{"APIVersion":"5.7.0","Version":"5.7.0"}}`

func TestParseEngineInfo(t *testing.T) {
	d, ok := parseEngineInfo([]byte(dockerInfoMac))
	if !ok || d.MemBytes != 18829856768 || d.CPUs != 14 {
		t.Fatalf("docker = %+v, %v", d, ok)
	}
	want := EngineIdentity{System: "Docker Desktop", Kernel: "7.0.14-linuxkit", ID: "b7d1c5a2", Name: "docker-desktop", Arch: "aarch64"}
	if d.Identity != want {
		t.Errorf("docker identity = %+v\nwant %+v", d.Identity, want)
	}
	if len(d.Labels) != 1 || !strings.Contains(d.Labels[0], "/Users/me/") {
		t.Errorf("docker labels = %q", d.Labels)
	}
	p, ok := parseEngineInfo([]byte(podmanInfoKrun))
	if !ok || p.MemBytes != 2006432768 || p.CPUs != 7 {
		t.Fatalf("podman = %+v, %v", p, ok)
	}
	wantP := EngineIdentity{System: "fedora 43", Kernel: "6.17.7-300.fc43.aarch64", Name: "localhost.localdomain", Arch: "arm64"}
	if p.Identity != wantP {
		t.Errorf("podman identity = %+v\nwant %+v", p.Identity, wantP)
	}
	if _, ok := parseEngineInfo([]byte("Client:\n Version: 29.0.1\n")); ok {
		t.Error("plain text isn't engine info")
	}
}

func TestParseEngineSystem(t *testing.T) {
	sys, cdi := parseEngineSystem([]byte(dockerInfoMac))
	if sys != "Docker Desktop · kernel 7.0.14-linuxkit" {
		t.Errorf("docker system = %q", sys)
	}
	if !slices.Equal(cdi, []string{webGPUDevice}) {
		t.Errorf("docker cdi = %q", cdi)
	}

	sys, cdi = parseEngineSystem([]byte(podmanInfoKrun))
	if sys != "fedora 43 · kernel 6.17.7-300.fc43.aarch64" {
		t.Errorf("podman system = %q", sys)
	}
	if len(cdi) != 0 {
		t.Errorf("podman cdi = %q", cdi)
	}

	linux := `{"OperatingSystem":"Ubuntu 24.04.3 LTS","KernelVersion":"6.8.0-88-generic","DiscoveredDevices":[{"Source":"cdi","ID":"nvidia.com/gpu=0"},{"Source":"cdi","ID":"nvidia.com/gpu=all"}]}`
	sys, cdi = parseEngineSystem([]byte(linux))
	if sys != "Ubuntu 24.04.3 LTS · kernel 6.8.0-88-generic" || !slices.Equal(cdi, []string{"nvidia.com/gpu=0", "nvidia.com/gpu=all"}) {
		t.Errorf("linux = %q %q", sys, cdi)
	}

	// Older engines without the JSON fields, or plain-text output.
	text := "Client:\n Version:    29.0.1\n\nServer:\n Kernel Version: 7.0.14-linuxkit\n Operating System: Docker Desktop\n" +
		" Discovered Devices:\n  cdi: docker.com/gpu=webgpu\n Insecure Registries:\n  hubproxy.docker.internal:5555\n"
	sys, cdi = parseEngineSystem([]byte(text))
	if sys != "" || !slices.Equal(cdi, []string{webGPUDevice}) {
		t.Errorf("text = %q %q", sys, cdi)
	}
	if got := parseDiscoveredDevicesText("Server:\n Containers: 3\n"); len(got) != 0 {
		t.Errorf("no section = %q", got)
	}
}

func TestGPUVerdict(t *testing.T) {
	mac := verdictHost{OS: "darwin"}
	macKrun := verdictHost{OS: "darwin", FactoryGPU: "vulkan"}
	nvidia := verdictHost{OS: "linux", FactoryGPU: "cuda"}
	cases := []struct {
		name string
		p    GPUProbe
		h    verdictHost
		want []string
		not  []string
	}{
		{"down", GPUProbe{Engine: "podman"}, mac, []string{"Not reachable"}, nil},
		{"krun podman", GPUProbe{Engine: "podman", Reachable: true, DRI: true, RenderAccess: "all"}, macKrun, []string{"Vulkan images can use the GPU"}, nil},
		{"docker vmm", GPUProbe{Engine: "docker", Reachable: true, DRI: true, RenderAccess: "all"}, mac,
			[]string{"builds and runs Vulkan images on Docker", "Docker VMM", "Experimental", "unverified", "Check again"}, nil},
		{"docker vmm, render group", GPUProbe{Engine: "docker", Reachable: true, DRI: true, RenderAccess: "group", RenderGID: 993}, mac,
			[]string{"builds and runs Vulkan images on Docker"}, nil},
		{"docker vmm, root-only render node", GPUProbe{Engine: "docker", Reachable: true, DRI: true, RenderAccess: "root"}, mac,
			[]string{"only root can open", "Podman (libkrun)"}, []string{"builds and runs"}},
		{"docker, no render node", GPUProbe{Engine: "docker", Reachable: true, DRI: true, RenderAccess: "none"}, verdictHost{OS: "linux"},
			[]string{"no render node", "another engine"}, []string{"builds and runs"}},
		{"linux docker on an nvidia host", GPUProbe{Engine: "docker", Reachable: true, DRI: true, RenderAccess: "all"}, nvidia,
			[]string{"CUDA"}, []string{"builds and runs"}},
		{"linux docker, amd", GPUProbe{Engine: "docker", Reachable: true, DRI: true, RenderAccess: "all"}, verdictHost{OS: "linux"},
			[]string{"builds and runs Vulkan images on Docker"}, []string{"Docker VMM"}},
		{"docker desktop avf", GPUProbe{Engine: "docker", Reachable: true, WebGPU: true}, mac, []string{"needs Dawn", "Docker VMM"}, nil},
		{"applehv podman", GPUProbe{Engine: "podman", Reachable: true}, mac, []string{"isn't a libkrun"}, nil},
		{"mac docker, no gpu", GPUProbe{Engine: "docker", Reachable: true}, mac, []string{"run on the CPU", "Docker VMM", "check again"}, nil},
		{"plain docker", GPUProbe{Engine: "docker", Reachable: true}, verdictHost{OS: "linux"}, []string{"run on the CPU"}, []string{"Docker VMM"}},
	}
	for _, c := range cases {
		got := gpuVerdict(c.p, c.h)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: verdict %q lacks %q", c.name, got, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(got, n) {
				t.Errorf("%s: verdict %q says %q", c.name, got, n)
			}
		}
	}
}

func TestUsableRender(t *testing.T) {
	cases := []struct {
		p    GPUProbe
		want bool
	}{
		{GPUProbe{DRI: true, RenderAccess: "all"}, true},
		{GPUProbe{DRI: true, RenderAccess: "group", RenderGID: 44}, true},
		{GPUProbe{DRI: true}, true}, // couldn't tell: as before modes were listed
		{GPUProbe{DRI: true, RenderAccess: "root"}, false},
		{GPUProbe{DRI: true, RenderAccess: "none"}, false},
		{GPUProbe{DRI: false, RenderAccess: "all"}, false},
	}
	for _, c := range cases {
		if got := c.p.usableRender(); got != c.want {
			t.Errorf("usableRender(%+v) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestProbeErrText(t *testing.T) {
	if got := probeErrText(errors.New("exec: \"podman\": executable file not found in $PATH")); !strings.Contains(got, "not found") {
		t.Errorf("plain error = %q", got)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	// A local shell standing in for the engine CLI: the last stderr line wins.
	_, err = exec.Command(sh, "-c", `echo "Unable to find image locally" >&2; echo 'docker: Error response from daemon: error gathering device information while adding custom device "/dev/dri": no such file or directory.' >&2; exit 125`).Output()
	got := probeErrText(err)
	if !strings.HasPrefix(got, "docker: Error response from daemon: error gathering device information") {
		t.Errorf("exit error = %q", got)
	}
}
