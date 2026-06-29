package server

import (
	"context"
	"fmt"
	"net/http"
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
	size, ram, vram := 4.0, 4.0, 4.0
	if m := s.modelByID(modelID); m != nil {
		size, ram, vram = m.SizeGB, m.MinRAMGB, m.MinVRAMGB
	}
	if ram <= 0 {
		ram = size
	}
	if compute != "cuda" && compute != "vulkan" {
		return Footprint{VRAMGB: 0, RAMGB: ram} // CPU: no VRAM, weights in RAM
	}
	if vram <= 0 {
		vram = size
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
// Committed = summed footprints of the model containers THIS factory manages;
// GPU/RAM used by processes outside the factory (e.g. a model run by hand) isn't
// visible here, so Free is an upper bound — the UI notes that.
type ResourceBudget struct {
	GPU             string         `json:"gpu"`
	TotalVRAMGB     float64        `json:"total_vram_gb"`
	TotalRAMGB      float64        `json:"total_ram_gb"`
	CommittedVRAMGB float64        `json:"committed_vram_gb"`
	CommittedRAMGB  float64        `json:"committed_ram_gb"`
	FreeVRAMGB      float64        `json:"free_vram_gb"`
	FreeRAMGB       float64        `json:"free_ram_gb"`
	Running         []RunningModel `json:"running"`
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
	b.FreeVRAMGB = si.VRAMGB - b.CommittedVRAMGB
	b.FreeRAMGB = si.MemGB - b.CommittedRAMGB
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
		return fmt.Sprintf("Needs ~%.1f GB VRAM but only %.1f of %.1f GB is free (%.1f GB committed by %d running model%s). Stop a model, or run this one on CPU.",
			fp.VRAMGB, b.FreeVRAMGB, b.TotalVRAMGB, b.CommittedVRAMGB, len(b.Running), plural(len(b.Running))), "insufficient_vram"
	}
	if b.TotalRAMGB > 0 && fp.RAMGB > b.FreeRAMGB+0.5 {
		return fmt.Sprintf("Needs ~%.1f GB RAM but only %.1f of %.1f GB is free (%.1f GB committed). Stop a model first.",
			fp.RAMGB, b.FreeRAMGB, b.TotalRAMGB, b.CommittedRAMGB), "insufficient_ram"
	}
	return "", ""
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// handleResources returns the live VRAM/RAM budget so the UI can show usage and
// pre-warn before a Run.
func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.resourceBudget(ctx))
}
