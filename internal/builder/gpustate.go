package builder

// Saved GPU check results (config/gpu-probe.json). The start scripts can only
// see the Podman libkrun machine (FACTORY_GPU=vulkan) and NVIDIA cards; whether
// Docker's VM gives containers a GPU — Docker Desktop's Docker VMM, Docker
// Engine in a krunkit Lima VM — only a container can tell, so the GPU check's
// last answer per engine is kept here with the engine's identity at the time
// (system, kernel, daemon ID). While that identity still matches, an engine
// whose check saw a usable render node gives Vulkan (engineGPU). When it
// doesn't (another VM, a new kernel), the result is stale: the GPU is withheld
// and the System page asks for a new check. A Vulkan run that finds no
// /dev/dri saves that too, so routing stops sending GPU work there.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const gpuStateFile = "gpu-probe.json"

// Where a saved result came from.
const (
	gpuCheckProbe = "probe" // Check GPU access (ProbeGPU)
	gpuCheckRun   = "run"   // a Vulkan run on the engine failed for want of /dev/dri
)

// EngineIdentity is what `<engine> info` says about the machine behind an
// engine. Switching Docker Desktop's VM manager, or pointing the factory at
// another VM's socket, usually changes some of it.
type EngineIdentity struct {
	System string `json:"system,omitempty"` // "Docker Desktop", "fedora 43", "Ubuntu 24.04.3 LTS"
	Kernel string `json:"kernel,omitempty"`
	ID     string `json:"id,omitempty"`   // the Docker daemon's ID (podman has none)
	Name   string `json:"name,omitempty"` // the VM's host name
	Arch   string `json:"arch,omitempty"`
}

func (id EngineIdentity) known() bool { return id.System != "" || id.Kernel != "" || id.ID != "" }

// describe is the identity as one line: "Docker Desktop · kernel 7.0.14-linuxkit".
func (id EngineIdentity) describe() string {
	var parts []string
	if id.System != "" {
		parts = append(parts, id.System)
	}
	if id.Kernel != "" {
		parts = append(parts, "kernel "+id.Kernel)
	}
	return strings.Join(parts, " · ")
}

// staleReason says why a check saved on machine `saved` no longer describes
// machine `now` ("" when it still does, or when now is unknown: an engine that
// isn't answering keeps its result until it answers again).
func staleReason(saved, now EngineIdentity) string {
	if !now.known() {
		return ""
	}
	if !saved.known() {
		return "the check didn't record which machine it ran on"
	}
	changed := func(what, a, b string) string {
		if a == "" {
			a = "unknown"
		}
		return fmt.Sprintf("its %s changed (%s → %s)", what, a, b)
	}
	switch {
	case saved.ID != "" && now.ID != "" && saved.ID != now.ID:
		return "it's a different engine (its ID changed)"
	case saved.System != now.System:
		return changed("system", saved.System, now.System)
	case saved.Kernel != now.Kernel:
		return changed("kernel", saved.Kernel, now.Kernel)
	case saved.Name != "" && now.Name != "" && saved.Name != now.Name:
		return changed("host name", saved.Name, now.Name)
	case saved.Arch != "" && now.Arch != "" && saved.Arch != now.Arch:
		return changed("architecture", saved.Arch, now.Arch)
	}
	return ""
}

// GPUCheckRecord is one engine's saved result.
type GPUCheckRecord struct {
	CheckedAt time.Time      `json:"checked_at"`
	Source    string         `json:"source"` // gpuCheckProbe | gpuCheckRun
	Identity  EngineIdentity `json:"identity"`
	Probe     GPUProbe       `json:"probe"`
}

// gpuStateDoc is config/gpu-probe.json.
type gpuStateDoc struct {
	Engines map[string]GPUCheckRecord `json:"engines"`
}

// GPUCheckInfo is a saved result as /api/sysinfo reports it (EngineResources.GPUCheck).
type GPUCheckInfo struct {
	GPUProbe
	CheckedAt int64  `json:"checked_at"` // unix ms
	Source    string `json:"source"`     // "probe" | "run"
	Stale     bool   `json:"stale,omitempty"`
	StaleWhy  string `json:"stale_reason,omitempty"` // "its kernel changed (… → …)"
}

// checkInfo turns a saved record into what sysinfo reports, judged against the
// engine's identity now.
func checkInfo(rec GPUCheckRecord, now EngineIdentity) *GPUCheckInfo {
	why := staleReason(rec.Identity, now)
	return &GPUCheckInfo{
		GPUProbe:  rec.Probe,
		CheckedAt: rec.CheckedAt.UnixMilli(),
		Source:    rec.Source,
		Stale:     why != "",
		StaleWhy:  why,
	}
}

func (b *Builder) gpuStatePath() string {
	if b.ConfigDir == "" {
		return ""
	}
	return filepath.Join(b.ConfigDir, gpuStateFile)
}

// gpuChecksLocked returns the saved results, reading the file the first time.
// A missing or unreadable file means no results. Callers hold b.gpuMu.
func (b *Builder) gpuChecksLocked() map[string]GPUCheckRecord {
	if b.gpuState != nil {
		return b.gpuState
	}
	b.gpuState = map[string]GPUCheckRecord{}
	if p := b.gpuStatePath(); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			var doc gpuStateDoc
			if json.Unmarshal(data, &doc) == nil {
				for e, r := range doc.Engines {
					b.gpuState[resolveEngine(e)] = r
				}
			}
		}
	}
	return b.gpuState
}

// savedGPUCheck is engine's last saved result.
func (b *Builder) savedGPUCheck(engine string) (GPUCheckRecord, bool) {
	b.gpuMu.Lock()
	defer b.gpuMu.Unlock()
	r, ok := b.gpuChecksLocked()[resolveEngine(engine)]
	return r, ok
}

// saveGPUCheck records engine's result and writes the file (atomically: a
// temp file renamed over it, so a crash never leaves half a file).
func (b *Builder) saveGPUCheck(engine string, rec GPUCheckRecord) error {
	b.gpuMu.Lock()
	defer b.gpuMu.Unlock()
	all := b.gpuChecksLocked()
	all[resolveEngine(engine)] = rec
	p := b.gpuStatePath()
	if p == "" {
		return nil
	}
	data, err := json.MarshalIndent(gpuStateDoc{Engines: all}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".gpu-probe-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// probeGPU is the saved check's verdict on engine's GPU: it saw a render node
// the factory's images can open, on the machine the engine is now.
func probeGPU(rec GPUCheckRecord, have bool, now EngineIdentity) bool {
	return have && rec.Probe.usableRender() && staleReason(rec.Identity, now) == ""
}

// vulkanGroup is the group a Vulkan container on engine is added to so its
// unprivileged user can open the render node (-1 = none needed or known).
func vulkanGroup(rec GPUCheckRecord, have bool) int {
	if have && rec.Probe.DRI && rec.Probe.RenderAccess == "group" {
		return rec.Probe.RenderGID
	}
	return -1
}

// noteVulkanRunFailed saves that a Vulkan run on engine found no /dev/dri,
// when a saved check had said it would: the GPU is withheld from the engine
// until the next GPU check.
func (b *Builder) noteVulkanRunFailed(engine string, now EngineIdentity, detail, hint string) {
	rec, ok := b.savedGPUCheck(engine)
	if !ok || !rec.Probe.DRI {
		return
	}
	if !now.known() {
		now = rec.Identity
	}
	p := rec.Probe
	p.DRI, p.DRIDevices, p.RenderAccess, p.RenderGID = false, nil, "", 0
	p.DRIError, p.Verdict = firstLine(detail), hint
	_ = b.saveGPUCheck(engine, GPUCheckRecord{CheckedAt: time.Now(), Source: gpuCheckRun, Identity: now, Probe: p})
}
