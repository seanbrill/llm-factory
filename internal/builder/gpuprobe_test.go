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
	want := []string{"run", "--rm", "--network", "none", "--label", "local-llm.tool=gpu-probe", "--device", "/dev/dri", img, "ls", "-1", "/dev/dri"}
	if !slices.Equal(docker, want) {
		t.Errorf("docker args = %q\nwant %q", docker, want)
	}
	podman := gpuProbeArgs("podman", img)
	wantP := []string{"run", "--rm", "--network", "none", "--label", "local-llm.tool=gpu-probe", "--device", "/dev/dri",
		"--security-opt", "seccomp=unconfined", "--security-opt", "label=disable", img, "ls", "-1", "/dev/dri"}
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

func TestParseDRIList(t *testing.T) {
	if got := parseDRIList("by-path\ncard0\nrenderD128\n"); !slices.Equal(got, []string{"by-path", "card0", "renderD128"}) {
		t.Errorf("got %q", got)
	}
	if got := parseDRIList(""); len(got) != 0 {
		t.Errorf("empty listing = %q", got)
	}
	if got := parseDRIList("  \nrenderD128\r\n"); !slices.Equal(got, []string{"renderD128"}) {
		t.Errorf("blank lines/CRLF = %q", got)
	}
}

// dockerInfoMac is trimmed `docker info --format '{{json .}}'` from Docker
// Desktop 4.96 on an M4 Pro (Apple Virtualization).
const dockerInfoMac = `{"ID":"b7d1c5a2","Containers":3,"Driver":"overlayfs","NCPU":14,"MemTotal":18829856768,
"OperatingSystem":"Docker Desktop","OSType":"linux","Architecture":"aarch64","KernelVersion":"7.0.14-linuxkit",
"CDISpecDirs":["/etc/cdi","/var/run/cdi"],"DiscoveredDevices":[{"Source":"cdi","ID":"docker.com/gpu=webgpu"}],
"Labels":["com.docker.desktop.address=unix:///Users/me/Library/Containers/com.docker.docker/Data/docker-cli.sock"]}`

// podmanInfoKrun is trimmed `podman info --format '{{json .}}'` from a libkrun
// Podman machine.
const podmanInfoKrun = `{"host":{"arch":"arm64","cpus":7,"memTotal":2006432768,
"distribution":{"distribution":"fedora","variant":"coreos","version":"43"},"kernel":"6.17.7-300.fc43.aarch64",
"os":"linux","remoteSocket":{"path":"/run/podman/podman.sock","exists":true}},"store":{"graphDriverName":"overlay"},
"version":{"APIVersion":"5.7.0","Version":"5.7.0"}}`

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
	cases := []struct {
		name string
		p    GPUProbe
		want string
	}{
		{"down", GPUProbe{Engine: "podman"}, "Not reachable"},
		{"krun podman", GPUProbe{Engine: "podman", Reachable: true, DRI: true}, "Vulkan images can use the GPU"},
		{"docker vmm", GPUProbe{Engine: "docker", Reachable: true, DRI: true}, "unverified"},
		{"docker desktop avf", GPUProbe{Engine: "docker", Reachable: true, WebGPU: true}, "needs Dawn"},
		{"applehv podman", GPUProbe{Engine: "podman", Reachable: true}, "isn't a libkrun"},
		{"plain docker", GPUProbe{Engine: "docker", Reachable: true}, "run on the CPU"},
	}
	for _, c := range cases {
		if got := gpuVerdict(c.p); !strings.Contains(got, c.want) {
			t.Errorf("%s: verdict %q lacks %q", c.name, got, c.want)
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
