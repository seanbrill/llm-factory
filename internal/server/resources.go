package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yourorg/local-llm/internal/catalog"
)

// Footprint estimates the VRAM/RAM a model occupies when running on a compute
// target, from the curated catalog numbers.
type Footprint struct {
	VRAMGB float64 `json:"vram_gb"`
	RAMGB  float64 `json:"ram_gb"`
}

func (s *Server) modelByID(id string) *catalog.Model {
	if id == "" || s.cat == nil {
		return nil
	}
	for i := range s.cat.Models {
		if s.cat.Models[i].ID == id {
			return &s.cat.Models[i]
		}
	}
	return nil
}

// footprint estimates a model's resource cost. On GPU the (quantized) weights +
// compute sit in VRAM (min_vram_gb, with size as a floor); the host still uses
// RAM (min_ram_gb). On CPU there's no VRAM and the weights live in RAM. Unknown
// models get a conservative 4 GB default so the guardrail still does something.
func (s *Server) footprint(modelID, compute string) Footprint {
	size, ram, vram, runtime := 4.0, 4.0, 4.0, "cpp"
	if m := s.modelByID(modelID); m != nil {
		size, ram, vram, runtime = m.SizeGB, m.MinRAMGB, m.MinVRAMGB, m.Rt()
	}
	if ram <= 0 {
		ram = size
	}
	if compute != "cuda" && compute != "vulkan" {
		return Footprint{VRAMGB: 0, RAMGB: ram} // CPU: no VRAM, weights in RAM
	}
	if vram <= 0 {
		// cpp (GGUF) keeps the quantized weights in VRAM (~size); a python/fp16
		// model is unquantized and needs roughly double its on-disk GGUF-equivalent.
		vram = size
		if runtime == "python" {
			vram = size * 2
		}
	}
	return Footprint{VRAMGB: vram, RAMGB: ram}
}

// RunningModel is one running container's estimated cost, for the budget view.
type RunningModel struct {
	Name    string  `json:"name"`
	ModelID string  `json:"model_id"`
	Compute string  `json:"compute"`
	VRAMGB  float64 `json:"vram_gb"`
	RAMGB   float64 `json:"ram_gb"`
}

// ResourceBudget is the live resource picture used for run guardrails + the UI.
// Used/Free are GLOBAL (the whole machine): VRAM from nvidia-smi, RAM from
// /proc/meminfo, so models run OUTSIDE the factory count too. Committed is the
// factory-managed subset (for the per-model breakdown). GlobalVRAM/GlobalRAM say
// whether Used is a real measurement (true) or fell back to the estimate (false).
type ResourceBudget struct {
	GPU             string         `json:"gpu"`
	TotalVRAMGB     float64        `json:"total_vram_gb"`
	TotalRAMGB      float64        `json:"total_ram_gb"`
	UsedVRAMGB      float64        `json:"used_vram_gb"`
	UsedRAMGB       float64        `json:"used_ram_gb"`
	FreeVRAMGB      float64        `json:"free_vram_gb"`
	FreeRAMGB       float64        `json:"free_ram_gb"`
	CommittedVRAMGB float64        `json:"committed_vram_gb"`
	CommittedRAMGB  float64        `json:"committed_ram_gb"`
	GlobalVRAM      bool           `json:"global_vram"`
	GlobalRAM       bool           `json:"global_ram"`
	// CPU is global too: 1-min load average from /proc/loadavg vs core count.
	CPUs       int     `json:"cpus"`
	CPULoad1   float64 `json:"cpu_load1"`
	CPUUsedPct float64 `json:"cpu_used_pct"`
	Running    []RunningModel `json:"running"`
}

// procCPU reads the 1-min load average from /proc/loadavg (host/VM, not
// namespaced — so it reflects ALL processes' CPU pressure).
func procCPU() (load1 float64, ok bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(data))
	if len(f) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// procRAMUsedGB reads global RAM usage from /proc/meminfo. Inside a container
// this reflects the host/VM (memory isn't namespaced), so it captures RAM used
// by everything — including processes outside the factory.
func procRAMUsedGB() (used, total float64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	var totalKB, availKB float64
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(f[1], 64) // value is in kB
		switch f[0] {
		case "MemTotal:":
			totalKB = v
		case "MemAvailable:":
			availKB = v
		}
	}
	if totalKB <= 0 {
		return 0, 0, false
	}
	return (totalKB - availKB) / 1e6, totalKB / 1e6, true // kB -> GB
}

// gpuUsed returns global VRAM used (GB), cached ~6s since the nvidia-smi query
// spins up a throwaway container.
func (s *Server) gpuUsedGB(ctx context.Context) (used, total float64, ok bool) {
	s.gpuMu.Lock()
	if s.gpuOK && time.Since(s.gpuAt) < 6*time.Second {
		u, t := s.gpuUsed, s.gpuTotal
		s.gpuMu.Unlock()
		return u, t, true
	}
	s.gpuMu.Unlock()
	u, t, k := s.b.GPUUsedGB(ctx)
	if k {
		s.gpuMu.Lock()
		s.gpuUsed, s.gpuTotal, s.gpuOK, s.gpuAt = u, t, true, time.Now()
		s.gpuMu.Unlock()
	}
	return u, t, k
}

// cLabel reads a key from a container row's docker-style "k=v,k=v" Labels string.
func cLabel(c map[string]any, key string) string {
	s, _ := c["Labels"].(string)
	for _, part := range strings.Split(s, ",") {
		if eq := strings.IndexByte(part, '='); eq > 0 && part[:eq] == key {
			return part[eq+1:]
		}
	}
	return ""
}

func cRunning(c map[string]any) bool {
	if st, _ := c["State"].(string); strings.EqualFold(st, "running") {
		return true
	}
	st, _ := c["Status"].(string)
	return strings.HasPrefix(st, "Up")
}

func (s *Server) resourceBudget(ctx context.Context) ResourceBudget {
	si := s.b.SysInfo(ctx)
	b := ResourceBudget{GPU: si.GPU, TotalVRAMGB: si.VRAMGB, TotalRAMGB: si.MemGB}

	// Factory-managed footprints (the per-model breakdown + the estimate fallback).
	cs, _ := s.b.Containers(ctx)
	for _, c := range cs {
		if !cRunning(c) {
			continue
		}
		modelID := cLabel(c, "local-llm.model")
		compute := cLabel(c, "local-llm.compute")
		fp := s.footprint(modelID, compute)
		name, _ := c["Names"].(string)
		b.CommittedVRAMGB += fp.VRAMGB
		b.CommittedRAMGB += fp.RAMGB
		b.Running = append(b.Running, RunningModel{
			Name: strings.TrimPrefix(name, "/"), ModelID: modelID, Compute: compute,
			VRAMGB: fp.VRAMGB, RAMGB: fp.RAMGB,
		})
	}

	// GLOBAL usage (whole machine) is the source of truth for Used/Free so the
	// guardrail accounts for anything running outside the factory. Fall back to the
	// committed estimate when a real measurement isn't available.
	if uv, tv, ok := s.gpuUsedGB(ctx); ok {
		b.UsedVRAMGB, b.GlobalVRAM = uv, true
		if tv > 0 {
			b.TotalVRAMGB = tv
		}
	} else {
		b.UsedVRAMGB = b.CommittedVRAMGB
	}
	if ur, tr, ok := procRAMUsedGB(); ok {
		b.UsedRAMGB, b.GlobalRAM = ur, true
		if tr > 0 {
			b.TotalRAMGB = tr
		}
	} else {
		b.UsedRAMGB = b.CommittedRAMGB
	}
	b.FreeVRAMGB = b.TotalVRAMGB - b.UsedVRAMGB
	b.FreeRAMGB = b.TotalRAMGB - b.UsedRAMGB

	// Global CPU pressure (1-min load vs cores).
	b.CPUs = si.CPUs
	if l, ok := procCPU(); ok && si.CPUs > 0 {
		b.CPULoad1 = l
		if p := l / float64(si.CPUs) * 100; p > 100 {
			b.CPUUsedPct = 100
		} else {
			b.CPUUsedPct = p
		}
	}
	return b
}

// runFits returns a non-empty (message, code) when starting modelID on compute
// would over-subscribe VRAM or RAM given what's already running. Small headroom
// allowances absorb estimate noise. Empty message => safe to start.
func (s *Server) runFits(ctx context.Context, modelID, compute string) (msg, code string) {
	fp := s.footprint(modelID, compute)
	b := s.resourceBudget(ctx)
	gpu := compute == "cuda" || compute == "vulkan"
	if gpu && b.TotalVRAMGB > 0 && fp.VRAMGB > b.FreeVRAMGB+0.25 {
		return fmt.Sprintf("Needs ~%.1f GB VRAM but only %.1f of %.1f GB is free (%.1f GB in use). Stop a model, free up the GPU, or run this one on CPU.",
			fp.VRAMGB, b.FreeVRAMGB, b.TotalVRAMGB, b.UsedVRAMGB), "insufficient_vram"
	}
	if b.TotalRAMGB > 0 && fp.RAMGB > b.FreeRAMGB+0.5 {
		return fmt.Sprintf("Needs ~%.1f GB RAM but only %.1f of %.1f GB is free (%.1f GB in use). Free up memory first.",
			fp.RAMGB, b.FreeRAMGB, b.TotalRAMGB, b.UsedRAMGB), "insufficient_ram"
	}
	// CPU saturation: a CPU-run model maxes the cores, so starting one onto an
	// already-busy CPU makes the whole machine lag (won't crash, but unusable).
	if !gpu && b.CPUs > 0 && b.CPUUsedPct >= 85 {
		return fmt.Sprintf("The CPU is already ~%.0f%% busy (load %.1f / %d cores). Running this on CPU will make the system lag — free up CPU first, or use GPU.",
			b.CPUUsedPct, b.CPULoad1, b.CPUs), "cpu_saturated"
	}
	return "", ""
}

// handleResources returns the live VRAM/RAM budget so the UI can show usage and
// pre-warn before a Run.
func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.resourceBudget(ctx))
}
