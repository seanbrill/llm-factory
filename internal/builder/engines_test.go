package builder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestEngineInfoFormat(t *testing.T) {
	// The bug: podman was asked for .Host.NCPU (docker's name), the template
	// failed, and the sidebar showed Docker's RAM next to Podman's GPU.
	if got := engineInfoFormat("podman"); got != "{{.Host.MemTotal}} {{.Host.CPUs}}" {
		t.Errorf("podman format = %q", got)
	}
	if got := engineInfoFormat("docker"); got != "{{.MemTotal}} {{.NCPU}}" {
		t.Errorf("docker format = %q", got)
	}
	if got := engineInfoFormat(""); got != "{{.MemTotal}} {{.NCPU}}" {
		t.Errorf("default engine format = %q", got)
	}
}

func TestParseMemCPU(t *testing.T) {
	cases := []struct {
		in   string
		mem  int64
		cpus int
		ok   bool
	}{
		{"2006432768 7\n", 2006432768, 7, true},
		{"18829856768 14", 18829856768, 14, true},
		{"<no value> <no value>", 0, 0, false},
		{"", 0, 0, false},
		{"0 4", 0, 0, false},
		{"123", 0, 0, false},
	}
	for _, c := range cases {
		mem, cpus, ok := parseMemCPU(c.in)
		if mem != c.mem || cpus != c.cpus || ok != c.ok {
			t.Errorf("parseMemCPU(%q) = %d, %d, %v; want %d, %d, %v", c.in, mem, cpus, ok, c.mem, c.cpus, c.ok)
		}
	}
}

func TestEngineGPU(t *testing.T) {
	cases := []struct {
		name               string
		factoryGPU, engine string
		probeDRI           bool
		want, from         string
	}{
		{"libkrun podman: the Mac GPU path", "vulkan", "podman", false, "vulkan", "host"},
		{"docker desktop, never checked", "vulkan", "docker", false, "", ""},
		{"docker vmm checked: same as podman", "vulkan", "docker", true, "vulkan", "probe"},
		{"docker in a krunkit VM, no podman", "", "docker", true, "vulkan", "probe"},
		{"host podman wins over its own probe", "vulkan", "podman", true, "vulkan", "host"},
		{"nvidia host: docker is cuda", "cuda", "docker", false, "cuda", "host"},
		{"nvidia host: cuda wins over /dev/dri", "cuda", "docker", true, "cuda", "host"},
		{"nvidia host: podman's /dev/dri is the nvidia card", "cuda", "podman", true, "", ""},
		{"linux amd/intel docker", "", "docker", true, "vulkan", "probe"},
		{"linux podman with a render node", "", "podman", true, "vulkan", "probe"},
		{"nothing", "", "docker", false, "", ""},
		{"nothing on podman", "", "podman", false, "", ""},
	}
	for _, c := range cases {
		got, from := engineGPU(c.factoryGPU, c.engine, c.probeDRI)
		if got != c.want || from != c.from {
			t.Errorf("%s: engineGPU(%q, %q, %v) = %q, %q; want %q, %q", c.name, c.factoryGPU, c.engine, c.probeDRI, got, from, c.want, c.from)
		}
	}
}

func TestSysInfoSummarize(t *testing.T) {
	docker := EngineResources{Engine: "docker", Reachable: true, MemGB: 18.8, CPUs: 14}
	podman := EngineResources{Engine: "podman", Reachable: true, MemGB: 2.0, CPUs: 7, GPU: "vulkan", GPUFrom: "host"}

	t.Run("mac with a small libkrun machine", func(t *testing.T) {
		si := SysInfo{Engines: []EngineResources{docker, podman}}
		si.summarize()
		// Top-level numbers now belong to the GPU engine, not Docker's RAM next
		// to Podman's GPU.
		if si.Engine != "podman" || si.MemGB != 2.0 || si.CPUs != 7 || si.GPU != "vulkan" {
			t.Errorf("top-level = %s %.1f GB %d CPU %q", si.Engine, si.MemGB, si.CPUs, si.GPU)
		}
		if !si.Engines[1].LowMemory || si.Engines[0].LowMemory {
			t.Errorf("low_memory = docker %v, podman %v; want only podman", si.Engines[0].LowMemory, si.Engines[1].LowMemory)
		}
		if si.DefaultEngine != "podman" || si.DefaultCompute != "vulkan" {
			t.Errorf("defaults = %s/%s, want podman/vulkan", si.DefaultEngine, si.DefaultCompute)
		}
	})
	t.Run("mac with a big libkrun machine", func(t *testing.T) {
		big := podman
		big.MemGB = 12.6
		si := SysInfo{Engines: []EngineResources{docker, big}}
		si.summarize()
		if si.Engines[1].LowMemory {
			t.Error("a 12.6 GB GPU machine shouldn't be flagged")
		}
	})
	t.Run("podman machine stopped", func(t *testing.T) {
		down := EngineResources{Engine: "podman", GPU: "vulkan", GPUFrom: "host"}
		si := SysInfo{Engines: []EngineResources{docker, down}}
		si.summarize()
		if si.Engine != "docker" || si.GPU != "" || si.MemGB != 18.8 {
			t.Errorf("top-level = %s %q %.1f, want docker's own numbers with no GPU", si.Engine, si.GPU, si.MemGB)
		}
		if si.Engines[1].LowMemory {
			t.Error("an unreachable engine isn't low on memory, it's down")
		}
		// Still the GPU path by default: the build then reports the engine is down.
		if si.DefaultEngine != "podman" || si.DefaultCompute != "vulkan" {
			t.Errorf("defaults = %s/%s, want podman/vulkan", si.DefaultEngine, si.DefaultCompute)
		}
	})
	t.Run("nvidia host", func(t *testing.T) {
		d := docker
		d.GPU, d.GPUFrom = "cuda", "host"
		si := SysInfo{Engines: []EngineResources{d}}
		si.summarize()
		if si.DefaultEngine != "docker" || si.DefaultCompute != "cuda" || si.GPU != "cuda" {
			t.Errorf("defaults = %s/%s gpu %q, want docker/cuda", si.DefaultEngine, si.DefaultCompute, si.GPU)
		}
	})
	t.Run("cpu only", func(t *testing.T) {
		si := SysInfo{Engines: []EngineResources{docker, {Engine: "podman", Reachable: true, MemGB: 4, CPUs: 4}}}
		si.summarize()
		if si.DefaultEngine != "docker" || si.DefaultCompute != "cpu" || si.Engine != "docker" {
			t.Errorf("defaults = %s/%s (top %s), want docker/cpu", si.DefaultEngine, si.DefaultCompute, si.Engine)
		}
	})
	t.Run("only podman answers", func(t *testing.T) {
		si := SysInfo{Engines: []EngineResources{{Engine: "docker"}, {Engine: "podman", Reachable: true, MemGB: 8, CPUs: 4}}}
		si.summarize()
		if si.DefaultEngine != "podman" || si.DefaultCompute != "cpu" {
			t.Errorf("defaults = %s/%s, want podman/cpu", si.DefaultEngine, si.DefaultCompute)
		}
	})
	t.Run("docker vmm and no podman", func(t *testing.T) {
		d := docker
		d.GPU, d.GPUFrom = "vulkan", "probe"
		si := SysInfo{Engines: []EngineResources{d}}
		si.summarize()
		if si.DefaultEngine != "docker" || si.DefaultCompute != "vulkan" || si.GPU != "vulkan" || si.Engines[0].LowMemory {
			t.Errorf("defaults = %s/%s gpu %q low %v, want docker/vulkan", si.DefaultEngine, si.DefaultCompute, si.GPU, si.Engines[0].LowMemory)
		}
	})
}

// Two engines give containers the GPU (a libkrun Podman machine and Docker
// VMM): GPU work defaults to the one with more usable memory.
func TestTwoGPUEngines(t *testing.T) {
	gpu := func(engine string, up bool, mem float64, from string) EngineResources {
		e := EngineResources{Engine: engine, Reachable: up, GPU: "vulkan", GPUFrom: from}
		if up {
			e.MemGB, e.CPUs = mem, 8
		}
		return e
	}
	cases := []struct {
		name       string
		docker     EngineResources
		podman     EngineResources
		want       string
		lowDocker  bool
		lowPodman  bool
		topMem     float64
		wantGPUTop string
	}{
		{"docker vmm bigger than a 2 GB podman", gpu("docker", true, 18.8, "probe"), gpu("podman", true, 2.0, "host"), "docker", false, true, 18.8, "vulkan"},
		{"podman bigger than docker", gpu("docker", true, 8.0, "probe"), gpu("podman", true, 12.6, "host"), "podman", false, false, 12.6, "vulkan"},
		{"same size: the verified libkrun path", gpu("docker", true, 12.0, "probe"), gpu("podman", true, 12.0, "host"), "podman", false, false, 12.0, "vulkan"},
		{"podman stopped", gpu("docker", true, 8.0, "probe"), gpu("podman", false, 0, "host"), "docker", false, false, 8.0, "vulkan"},
		{"docker stopped", gpu("docker", false, 0, "probe"), gpu("podman", true, 4.0, "host"), "podman", false, true, 4.0, "vulkan"},
		{"both small: still the bigger", gpu("docker", true, 5.0, "probe"), gpu("podman", true, 2.0, "host"), "docker", true, true, 5.0, "vulkan"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			si := SysInfo{Engines: []EngineResources{c.docker, c.podman}}
			si.summarize()
			if si.DefaultEngine != c.want || si.DefaultCompute != "vulkan" {
				t.Errorf("defaults = %s/%s, want %s/vulkan", si.DefaultEngine, si.DefaultCompute, c.want)
			}
			if si.Engine != c.want || si.MemGB != c.topMem || si.GPU != c.wantGPUTop {
				t.Errorf("top-level = %s %.1f %q", si.Engine, si.MemGB, si.GPU)
			}
			if si.Engines[0].LowMemory != c.lowDocker || si.Engines[1].LowMemory != c.lowPodman {
				t.Errorf("low_memory = docker %v podman %v, want %v %v", si.Engines[0].LowMemory, si.Engines[1].LowMemory, c.lowDocker, c.lowPodman)
			}
			if g := preferredGPUEngine(si.Engines, "vulkan"); g == nil || g.Engine != c.want {
				t.Errorf("preferredGPUEngine(vulkan) = %v", g)
			}
			if g := preferredGPUEngine(si.Engines, "cuda"); g != nil {
				t.Errorf("preferredGPUEngine(cuda) = %s, want none", g.Engine)
			}
		})
	}
}

// A small Docker VMM borrows Podman to compile, just like a small Podman
// machine borrows Docker: decideBuild doesn't care which engine has the GPU.
func TestDecideBuildDockerGPU(t *testing.T) {
	mem := func(gb float64) engineMem { return engineMem{GB: gb} }
	plan, err := decideBuild("docker", "vulkan", 6, map[string]engineMem{"docker": mem(4.0), "podman": mem(12.6)})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Build != "podman" || plan.Target != "docker" || !plan.Transfer {
		t.Errorf("plan = %+v, want build on podman, transfer to docker", plan)
	}
	if !strings.Contains(plan.Note, "Docker has 4.0 GB") || !strings.Contains(plan.Note, "compiles on the Podman machine") {
		t.Errorf("note = %q", plan.Note)
	}
	_, err = decideBuild("docker", "vulkan", 6, map[string]engineMem{"docker": mem(4.0), "podman": mem(2.0)})
	if err == nil || !strings.Contains(err.Error(), "Docker Desktop → Settings → Resources → Memory") {
		t.Errorf("err = %v, want the Docker memory fix", err)
	}
}

func TestCompileFloor(t *testing.T) {
	if compileFloorGB("vulkan") != 6 || compileFloorGB("cuda") != 6 || compileFloorGB("cpu") != 3 || compileFloorGB("") != 3 {
		t.Errorf("floors: vulkan %v cuda %v cpu %v", compileFloorGB("vulkan"), compileFloorGB("cuda"), compileFloorGB("cpu"))
	}
}

func TestDecideBuild(t *testing.T) {
	refused := errors.New("Cannot connect to Podman: connection refused")
	mem := func(gb float64) engineMem { return engineMem{GB: gb} }

	t.Run("small podman machine borrows docker", func(t *testing.T) {
		// Last night's failure: a 2 GiB, 7-CPU Podman machine compiling Vulkan.
		plan, err := decideBuild("podman", "vulkan", 6, map[string]engineMem{"podman": mem(2.0), "docker": mem(18.8)})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Build != "docker" || plan.Target != "podman" || !plan.Transfer || plan.BuildGB != 18.8 {
			t.Errorf("plan = %+v, want build on docker, transfer to podman", plan)
		}
		if !strings.Contains(plan.Note, "compiles on Docker") {
			t.Errorf("note = %q", plan.Note)
		}
	})
	t.Run("big enough target builds in place", func(t *testing.T) {
		plan, err := decideBuild("podman", "vulkan", 6, map[string]engineMem{"podman": mem(12.6)})
		if err != nil || plan.Build != "podman" || plan.Transfer {
			t.Errorf("plan = %+v, %v; want build on podman", plan, err)
		}
	})
	t.Run("cpu needs less", func(t *testing.T) {
		plan, err := decideBuild("docker", "cpu", 3, map[string]engineMem{"docker": mem(4)})
		if err != nil || plan.Build != "docker" || plan.Transfer {
			t.Errorf("plan = %+v, %v; want build on docker", plan, err)
		}
	})
	t.Run("small docker borrows podman", func(t *testing.T) {
		plan, err := decideBuild("docker", "cpu", 3, map[string]engineMem{"docker": mem(2), "podman": mem(8)})
		if err != nil || plan.Build != "podman" || plan.Target != "docker" || !plan.Transfer {
			t.Errorf("plan = %+v, %v; want build on podman, transfer to docker", plan, err)
		}
	})
	t.Run("nothing big enough fails with the fix", func(t *testing.T) {
		_, err := decideBuild("podman", "vulkan", 6, map[string]engineMem{"podman": mem(2.0), "docker": mem(4.2)})
		if err == nil {
			t.Fatal("want an error")
		}
		msg := err.Error()
		for _, want := range []string{
			"Not enough memory", "at least 6 GB", "the Podman machine has 2.0 GB", "Docker has 4.2 GB",
			"podman machine stop && podman machine set --memory 12288 && podman machine start",
			"Docker Desktop → Settings → Resources → Memory", "restart the factory",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("error lacks %q:\n%s", want, msg)
			}
		}
		// It must read as itself, not be mistaken for another engine failure.
		if hint := ClassifyEngineError(err); hint != "" {
			t.Errorf("ClassifyEngineError re-labelled it as %q", hint)
		}
	})
	t.Run("other engine unavailable", func(t *testing.T) {
		_, err := decideBuild("podman", "vulkan", 6, map[string]engineMem{"podman": mem(2.0), "docker": {Err: refused}})
		if err == nil || !strings.Contains(err.Error(), "Docker isn't available") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unreachable target fails now", func(t *testing.T) {
		_, err := decideBuild("podman", "vulkan", 6, map[string]engineMem{"podman": {Err: refused}, "docker": mem(18.8)})
		if err == nil || !strings.Contains(ClassifyEngineError(err), "unreachable") {
			t.Errorf("err = %v (hint %q), want the engine-unreachable hint", err, ClassifyEngineError(err))
		}
	})
	t.Run("unreadable memory builds anyway", func(t *testing.T) {
		plan, err := decideBuild("docker", "vulkan", 6, map[string]engineMem{"docker": {Err: errors.New(`template: :1: can't evaluate field NCPU`)}})
		if err != nil || plan.Build != "docker" || plan.Note == "" {
			t.Errorf("plan = %+v, %v; want build on docker with a note", plan, err)
		}
	})
}

func TestLocalName(t *testing.T) {
	cases := []struct{ engine, ref, want string }{
		{"podman", "local-llm/gemma-4-12b-general-use:latest", "localhost/local-llm/gemma-4-12b-general-use:latest"},
		{"podman", "foo:latest", "localhost/foo:latest"},
		{"podman", "localhost/foo:latest", "localhost/foo:latest"},
		{"podman", "quay.io/me/foo:1", "quay.io/me/foo:1"},
		{"podman", "registry:5000/foo:1", "registry:5000/foo:1"},
		{"docker", "local-llm/foo:latest", "local-llm/foo:latest"},
	}
	for _, c := range cases {
		if got := localName(c.engine, c.ref); got != c.want {
			t.Errorf("localName(%s, %s) = %s, want %s", c.engine, c.ref, got, c.want)
		}
	}
}

func TestLoadedName(t *testing.T) {
	ref := "local-llm/gemma-4-12b-general-use:latest"
	podman := []string{
		"Getting image source signatures",
		"Copying blob sha256:4f4fb700ef54",
		"Writing manifest to image destination",
		"Loaded image: docker.io/local-llm/gemma-4-12b-general-use:latest",
	}
	if got := loadedName(podman, ref); got != "docker.io/"+ref {
		t.Errorf("podman load: got %q", got)
	}
	if got := loadedName([]string{"Loaded image: " + ref}, ref); got != ref {
		t.Errorf("docker load: got %q", got)
	}
	if got := loadedName([]string{"Loaded image: localhost/" + ref}, ref); got != "localhost/"+ref {
		t.Errorf("docker loading a podman archive: got %q", got)
	}
	old := []string{"Loaded image(s): docker.io/library/other:1,docker.io/" + ref}
	if got := loadedName(old, ref); got != "docker.io/"+ref {
		t.Errorf("old podman: got %q", got)
	}
	if got := loadedName([]string{"Loaded image ID: sha256:abc"}, ref); got != "" {
		t.Errorf("id only: got %q, want \"\"", got)
	}
	if got := loadedName(nil, ref); got != "" {
		t.Errorf("no output: got %q", got)
	}
}

func TestCompilerOOM(t *testing.T) {
	oom := []string{
		"#14 1712.3 [ 61%] Building CXX object ggml/src/ggml-vulkan/CMakeFiles/ggml-vulkan.dir/ggml-vulkan.cpp.o",
		"#14 1745.9 c++: fatal error: Killed signal terminated program cc1plus",
		"#14 1745.9 compilation terminated.",
		"ERROR: failed to solve: process \"/bin/sh -c cmake ...\" did not complete successfully: exit code: 2",
	}
	if !compilerOOM(oom) {
		t.Error("cc1plus kill not detected")
	}
	if compilerOOM([]string{"error: 'foo' was not declared in this scope"}) {
		t.Error("an ordinary compile error isn't an OOM")
	}
	hint := oomHint("podman", 2.0)
	for _, want := range []string{"ran out of memory", "the Podman machine (2.0 GB)", "podman machine set --memory 12288", "BUILD_JOBS"} {
		if !strings.Contains(hint, want) {
			t.Errorf("oomHint lacks %q: %s", want, hint)
		}
	}
	err := &HintError{Hint: hint, Err: errors.New("podman build failed: exit status 1")}
	if got := ClassifyEngineError(fmt.Errorf("wrapped: %w", err)); got != hint {
		t.Errorf("ClassifyEngineError(HintError) = %q", got)
	}
}

func TestGPUDeviceHint(t *testing.T) {
	dockerOut := `docker: Error response from daemon: error gathering device information while adding custom device "/dev/dri": no such file or directory.`
	podmanOut := `Error: stat /dev/dri: no such file or directory`

	// Docker Desktop on a Mac: the actionable message — switch to Docker VMM,
	// then check again; or run it on Podman.
	mac := gpuDeviceHint("docker", "vulkan", dockerOut, dockerDesktopOnMac)
	for _, want := range []string{
		"Docker Desktop on this Mac", "Apple Virtualization", "Docker VMM",
		"Settings → General → Virtual Machine Manager", "Check GPU access", "run this image on Podman",
	} {
		if !strings.Contains(mac, want) {
			t.Errorf("mac docker hint lacks %q: %s", want, mac)
		}
	}
	lima := gpuDeviceHint("docker", "vulkan", dockerOut, dockerVMOnMac)
	for _, want := range []string{"krunkit", "Check GPU access", "run this image on Podman"} {
		if !strings.Contains(lima, want) {
			t.Errorf("lima docker hint lacks %q: %s", want, lima)
		}
	}
	if strings.Contains(lima, "Docker Desktop") {
		t.Errorf("a Lima VM isn't Docker Desktop: %s", lima)
	}
	if h := gpuDeviceHint("docker", "vulkan", dockerOut, dockerElsewhere); !strings.Contains(h, "/dev/dri") || strings.Contains(h, "Mac") {
		t.Errorf("linux docker hint = %q", h)
	}
	if h := gpuDeviceHint("podman", "vulkan", podmanOut, dockerElsewhere); !strings.Contains(h, "libkrun") {
		t.Errorf("podman hint = %q", h)
	}
	if h := gpuDeviceHint("docker", "cpu", dockerOut, dockerDesktopOnMac); h != "" {
		t.Errorf("a CPU run gets no GPU hint, got %q", h)
	}
	if h := gpuDeviceHint("docker", "vulkan", "port is already allocated", dockerDesktopOnMac); h != "" {
		t.Errorf("unrelated failure got %q", h)
	}
	// Without the Run-time context, the generic matcher still explains it.
	raw := errors.New("docker run: exit status 125: " + dockerOut)
	if h := ClassifyEngineError(raw); !strings.Contains(h, "no GPU device (/dev/dri)") || !strings.Contains(h, "Docker VMM") {
		t.Errorf("ClassifyEngineError(/dev/dri) = %q", h)
	}
	macInfo := `Docker Desktop|7.0.14-linuxkit|["com.docker.desktop.address=unix:///Users/me/Library/Containers/com.docker.docker/Data/docker-cli.sock"]`
	linuxDD := `Docker Desktop|6.12.54-linuxkit|["com.docker.desktop.address=unix:///home/me/.docker/desktop/docker-cli.sock"]`
	windows := `Docker Desktop|6.6.87.2-microsoft-standard-WSL2|["com.docker.desktop.address=npipe://\\\\.\\pipe\\docker_cli"]`
	engine := `Ubuntu 24.04.3 LTS|6.8.0-88-generic|[]`
	if !isDockerDesktopMac(macInfo) || isDockerDesktopMac(linuxDD) || isDockerDesktopMac(windows) || isDockerDesktopMac(engine) {
		t.Errorf("isDockerDesktopMac: mac %v, linux DD %v, windows %v, engine %v",
			isDockerDesktopMac(macInfo), isDockerDesktopMac(linuxDD), isDockerDesktopMac(windows), isDockerDesktopMac(engine))
	}
	// ...and the existing classifications are unchanged.
	if h := ClassifyEngineError(errors.New("Bind for 127.0.0.1:8080 failed: port is already allocated")); !strings.Contains(h, "port") {
		t.Errorf("port hint = %q", h)
	}
}

func TestDockerWhere(t *testing.T) {
	desktop, _ := parseEngineInfo([]byte(dockerInfoMac))
	lima, _ := parseEngineInfo([]byte(dockerInfoLima))
	linuxDD := engineInfo{Identity: EngineIdentity{System: "Docker Desktop", Kernel: "6.12.54-linuxkit"},
		Labels: []string{"com.docker.desktop.address=unix:///home/me/.docker/desktop/docker-cli.sock"}}
	cases := []struct {
		name   string
		hostOS string
		in     engineInfo
		want   dockerWhere
	}{
		{"docker desktop on a mac", "darwin", desktop, dockerDesktopOnMac},
		{"lima docker on a mac", "darwin", lima, dockerVMOnMac},
		{"docker desktop on a mac, host os unknown", "", desktop, dockerDesktopOnMac},
		{"lima, host os unknown", "", lima, dockerElsewhere},
		{"docker engine on linux", "linux", lima, dockerElsewhere},
		{"docker desktop on linux", "linux", linuxDD, dockerElsewhere},
		{"docker desktop on linux, host os unknown", "", linuxDD, dockerElsewhere},
		{"mac, docker didn't answer", "darwin", engineInfo{}, dockerVMOnMac},
	}
	for _, c := range cases {
		if got := dockerWhereOf(c.hostOS, c.in); got != c.want {
			t.Errorf("%s: dockerWhereOf = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCleanStaleBuildDirs(t *testing.T) {
	b := &Builder{BuildDir: t.TempDir()}
	for _, d := range []string{"ctx-1794634921257951000", "ens-17", "keep-me"} {
		if err := os.MkdirAll(filepath.Join(b.BuildDir, d, "gate"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"xfer-1.tar", "notes.txt", filepath.Join("ctx-1794634921257951000", "model.gguf")} {
		if err := os.WriteFile(filepath.Join(b.BuildDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	removed := b.CleanStaleBuildDirs()
	slices.Sort(removed)
	if want := []string{"ctx-1794634921257951000", "ens-17", "xfer-1.tar"}; !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	left, _ := os.ReadDir(b.BuildDir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"keep-me", "notes.txt"}) {
		t.Errorf("left %v", names)
	}
}

func TestAcquireBuildSlot(t *testing.T) {
	b := &Builder{}
	nop := func(string) {}
	release, err := b.acquireBuildSlot(context.Background(), "docker", nop)
	if err != nil {
		t.Fatal(err)
	}
	// The other engine's machine is independent.
	rel2, err := b.acquireBuildSlot(context.Background(), "podman", nop)
	if err != nil {
		t.Fatal(err)
	}
	rel2()
	// A second docker build waits — and gives up when its job is canceled.
	var logged []string
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.acquireBuildSlot(ctx, "docker", func(s string) { logged = append(logged, s) }); err == nil {
		t.Fatal("second docker slot should block until canceled")
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "Waiting") {
		t.Errorf("logged %v", logged)
	}
	release()
	got := make(chan error, 1)
	go func() {
		r, err := b.acquireBuildSlot(context.Background(), "docker", nop)
		if err == nil {
			r()
		}
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slot not released")
	}
}
