package builder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	vmmID = EngineIdentity{System: "Docker Desktop", Kernel: "7.0.14-linuxkit", ID: "b7d1c5a2", Name: "docker-desktop", Arch: "aarch64"}
	vmmOK = GPUProbe{Engine: "docker", Reachable: true, DRI: true, DRIDevices: []string{"card0", "renderD128"}, RenderAccess: "all", Verdict: "ok"}
)

func TestGPUCheckPersistence(t *testing.T) {
	dir := t.TempDir()
	b := &Builder{ConfigDir: dir}
	if _, ok := b.savedGPUCheck("docker"); ok {
		t.Fatal("a fresh factory has no saved check")
	}
	at := time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)
	rec := GPUCheckRecord{CheckedAt: at, Source: gpuCheckProbe, Identity: vmmID, Probe: vmmOK}
	if err := b.saveGPUCheck("docker", rec); err != nil {
		t.Fatal(err)
	}
	// The file is plain JSON keyed by engine, with no temp files left behind.
	data, err := os.ReadFile(filepath.Join(dir, "gpu-probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Engines map[string]struct {
			CheckedAt time.Time      `json:"checked_at"`
			Source    string         `json:"source"`
			Identity  EngineIdentity `json:"identity"`
			Probe     GPUProbe       `json:"probe"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("file isn't JSON: %v\n%s", err, data)
	}
	d := doc.Engines["docker"]
	if !d.CheckedAt.Equal(at) || d.Source != "probe" || d.Identity != vmmID || !d.Probe.DRI || d.Probe.RenderAccess != "all" {
		t.Errorf("saved = %+v", d)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 {
		t.Errorf("config dir has %d files, want only gpu-probe.json", len(left))
	}

	// A restarted factory reads it back.
	b2 := &Builder{ConfigDir: dir}
	got, ok := b2.savedGPUCheck("docker")
	if !ok || !got.CheckedAt.Equal(at) || got.Identity != vmmID || !slices.Equal(got.Probe.DRIDevices, vmmOK.DRIDevices) {
		t.Errorf("reloaded = %+v, %v", got, ok)
	}
	// Saving another engine keeps the first.
	if err := b2.saveGPUCheck("podman", GPUCheckRecord{CheckedAt: at, Source: gpuCheckProbe, Probe: GPUProbe{Engine: "podman", Reachable: true}}); err != nil {
		t.Fatal(err)
	}
	b3 := &Builder{ConfigDir: dir}
	if _, ok := b3.savedGPUCheck("docker"); !ok {
		t.Error("saving podman dropped docker")
	}
	if _, ok := b3.savedGPUCheck("podman"); !ok {
		t.Error("podman wasn't saved")
	}
}

func TestGPUCheckCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gpu-probe.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &Builder{ConfigDir: dir}
	if _, ok := b.savedGPUCheck("docker"); ok {
		t.Error("a broken file means no saved checks")
	}
	// The next check replaces it.
	if err := b.saveGPUCheck("docker", GPUCheckRecord{Source: gpuCheckProbe, Probe: vmmOK}); err != nil {
		t.Fatal(err)
	}
	if _, ok := (&Builder{ConfigDir: dir}).savedGPUCheck("docker"); !ok {
		t.Error("the new result wasn't readable")
	}
	// No config dir (tests, odd setups): kept in memory only.
	mem := &Builder{}
	if err := mem.saveGPUCheck("docker", GPUCheckRecord{Probe: vmmOK}); err != nil {
		t.Fatal(err)
	}
	if _, ok := mem.savedGPUCheck("docker"); !ok {
		t.Error("in-memory save lost")
	}
}

func TestStaleReason(t *testing.T) {
	newKernel := vmmID
	newKernel.Kernel = "7.1.2-linuxkit"
	otherDaemon := vmmID
	otherDaemon.ID = "c0ffee"
	lima := EngineIdentity{System: "Ubuntu 25.10", Kernel: "6.17.0-6-generic", ID: "0f6e2b9c", Name: "lima-docker", Arch: "aarch64"}
	renamed := vmmID
	renamed.Name = "docker-vmm"
	cases := []struct {
		name       string
		saved, now EngineIdentity
		want       string // "" = still fresh; else a fragment of the reason
	}{
		{"same machine", vmmID, vmmID, ""},
		{"engine down: can't tell, keep it", vmmID, EngineIdentity{}, ""},
		{"docker desktop updated its kernel", vmmID, newKernel, "kernel changed (7.0.14-linuxkit → 7.1.2-linuxkit)"},
		{"a different daemon", vmmID, otherDaemon, "different engine"},
		{"pointed at a lima VM", vmmID, lima, "different engine"},
		{"lima VM without an ID", EngineIdentity{System: "Ubuntu 25.10", Kernel: "6.17.0-6-generic"}, lima, ""},
		{"host name changed", vmmID, renamed, "host name changed"},
		{"saved without identity", EngineIdentity{}, vmmID, "didn't record"},
		{"system changed, no IDs", EngineIdentity{System: "fedora 42", Kernel: "6.1"}, EngineIdentity{System: "fedora 43", Kernel: "6.1"}, "system changed (fedora 42 → fedora 43)"},
	}
	for _, c := range cases {
		got := staleReason(c.saved, c.now)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: staleReason = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestProbeGPUFromSavedCheck(t *testing.T) {
	ok := GPUCheckRecord{Source: gpuCheckProbe, Identity: vmmID, Probe: vmmOK}
	group := ok
	group.Probe.RenderAccess, group.Probe.RenderGID = "group", 993
	root := ok
	root.Probe.RenderAccess = "root"
	none := ok
	none.Probe.DRI, none.Probe.DRIDevices = false, nil
	failedRun := ok
	failedRun.Source, failedRun.Probe.DRI = gpuCheckRun, false
	newKernel := vmmID
	newKernel.Kernel = "7.1.2-linuxkit"
	cases := []struct {
		name  string
		rec   GPUCheckRecord
		have  bool
		now   EngineIdentity
		gpu   bool
		group int
	}{
		{"never checked", GPUCheckRecord{}, false, vmmID, false, -1},
		{"render node for everyone", ok, true, vmmID, true, -1},
		{"render group", group, true, vmmID, true, 993},
		{"root only", root, true, vmmID, false, -1},
		{"no /dev/dri", none, true, vmmID, false, -1},
		{"a Vulkan run found none since", failedRun, true, vmmID, false, -1},
		{"machine changed: withheld", ok, true, newKernel, false, -1},
		{"engine down now: kept", ok, true, EngineIdentity{}, true, -1},
	}
	for _, c := range cases {
		if got := probeGPU(c.rec, c.have, c.now); got != c.gpu {
			t.Errorf("%s: probeGPU = %v, want %v", c.name, got, c.gpu)
		}
		if got := vulkanGroup(c.rec, c.have); got != c.group {
			t.Errorf("%s: vulkanGroup = %d, want %d", c.name, got, c.group)
		}
	}
}

func TestCheckInfo(t *testing.T) {
	at := time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)
	rec := GPUCheckRecord{CheckedAt: at, Source: gpuCheckProbe, Identity: vmmID, Probe: vmmOK}
	ci := checkInfo(rec, vmmID)
	if ci.CheckedAt != at.UnixMilli() || ci.Source != "probe" || ci.Stale || !ci.DRI || ci.Verdict != "ok" {
		t.Errorf("fresh = %+v", ci)
	}
	now := vmmID
	now.Kernel = "7.1.2-linuxkit"
	ci = checkInfo(rec, now)
	if !ci.Stale || !strings.Contains(ci.StaleWhy, "kernel") {
		t.Errorf("stale = %+v", ci)
	}
	// sysinfo flattens the probe's fields next to the check's.
	data, _ := json.Marshal(ci)
	for _, k := range []string{`"dri":true`, `"checked_at":`, `"source":"probe"`, `"stale":true`, `"stale_reason":`, `"render_access":"all"`} {
		if !strings.Contains(string(data), k) {
			t.Errorf("gpu_check JSON lacks %s: %s", k, data)
		}
	}
}

func TestNoteVulkanRunFailed(t *testing.T) {
	dir := t.TempDir()
	b := &Builder{ConfigDir: dir}
	// Nothing saved: nothing to correct.
	b.noteVulkanRunFailed("docker", vmmID, "no such file", "hint")
	if _, ok := b.savedGPUCheck("docker"); ok {
		t.Fatal("a failed run without a saved check shouldn't invent one")
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := b.saveGPUCheck("docker", GPUCheckRecord{CheckedAt: old, Source: gpuCheckProbe, Identity: vmmID, Probe: vmmOK}); err != nil {
		t.Fatal(err)
	}
	out := `docker: Error response from daemon: error gathering device information while adding custom device "/dev/dri": no such file or directory.` + "\nmore"
	b.noteVulkanRunFailed("docker", EngineIdentity{}, out, "Switch Docker Desktop to Docker VMM …")
	rec, ok := (&Builder{ConfigDir: dir}).savedGPUCheck("docker")
	if !ok || rec.Source != gpuCheckRun || rec.Probe.DRI || rec.Probe.RenderAccess != "" || len(rec.Probe.DRIDevices) != 0 {
		t.Fatalf("after the failed run = %+v", rec)
	}
	if !rec.CheckedAt.After(old) || rec.Identity != vmmID || !strings.HasPrefix(rec.Probe.DRIError, "docker: Error response") ||
		strings.Contains(rec.Probe.DRIError, "more") || !strings.Contains(rec.Probe.Verdict, "Docker VMM") {
		t.Errorf("after the failed run = %+v", rec)
	}
	if probeGPU(rec, true, vmmID) {
		t.Error("the GPU must be withheld after a run found no /dev/dri")
	}
}

func TestRunArgs(t *testing.T) {
	base := RunOptions{Ref: "local-llm/gemma:latest", HostPort: 8081, Route: "gemma"}
	with := func(engine, compute string) RunOptions {
		o := base
		o.Engine, o.Compute = engine, compute
		return o
	}
	cases := []struct {
		name   string
		engine string
		o      RunOptions
		group  int
		want   []string // in this order
		not    []string
	}{
		{"docker vulkan: just the render node", "docker", with("docker", "vulkan"), -1,
			[]string{"--device", "/dev/dri"}, []string{"--security-opt", "seccomp=unconfined", "label=disable", "--group-add", "--gpus"}},
		{"docker vulkan, render group", "docker", with("docker", "vulkan"), 993,
			[]string{"--device", "/dev/dri", "--group-add", "993"}, []string{"--security-opt"}},
		{"docker vulkan, root group", "docker", with("docker", "vulkan"), 0,
			[]string{"--device", "/dev/dri", "--group-add", "0"}, nil},
		{"podman vulkan: venus flags", "podman", with("podman", "vulkan"), -1,
			[]string{"--device", "/dev/dri", "--security-opt", "seccomp=unconfined", "--security-opt", "label=disable"}, []string{"--group-add"}},
		{"podman cpu: seccomp + label still", "podman", with("podman", "cpu"), -1,
			[]string{"--security-opt", "seccomp=unconfined", "--security-opt", "label=disable"}, []string{"--device"}},
		{"docker cuda", "docker", with("docker", "cuda"), -1, []string{"--gpus", "all"}, []string{"--device", "--security-opt"}},
		{"docker cpu", "docker", with("docker", "cpu"), 993, nil, []string{"--device", "--group-add", "--security-opt", "--gpus"}},
	}
	for _, c := range cases {
		args := runArgs(c.engine, "localllm-8081", c.o, c.group)
		if args[0] != "run" || args[len(args)-1] != base.Ref {
			t.Errorf("%s: args = %q", c.name, args)
		}
		if !slices.Contains(args, "local-llm.engine="+c.engine) || !slices.Contains(args, "127.0.0.1:8081:8080") {
			t.Errorf("%s: missing engine label or loopback port: %q", c.name, args)
		}
		if len(c.want) > 0 {
			i := slices.Index(args, c.want[0])
			if i < 0 || i+len(c.want) > len(args) || !slices.Equal(args[i:i+len(c.want)], c.want) {
				t.Errorf("%s: args lack %q in order: %q", c.name, c.want, args)
			}
		}
		for _, n := range c.not {
			if slices.Contains(args, n) {
				t.Errorf("%s: args have %q: %q", c.name, n, args)
			}
		}
	}
}
