package builder

// Batch preflight for the UI's build plan: before anything is queued, say for
// each model where its image will compile, whether it moves to another engine
// afterwards, and what would stop it — the same decision Build makes (see
// decideBuild), from one memory read per engine instead of one per model.

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/seanbrill/local-llm/internal/catalog"
)

// PreflightItem is one build the plan asks about.
type PreflightItem struct {
	Model   catalog.Model
	Engine  string // engine the image is for ("docker" | "podman")
	Compute string // requested compute ("cpu" | "cuda" | "vulkan")
}

// PreflightPlan is the answer for one item.
type PreflightPlan struct {
	Engine      string  `json:"engine"`       // engine the image is for
	Compute     string  `json:"compute"`      // compute it will actually build for
	Compiles    bool    `json:"compiles"`     // compiles a runtime from source
	BuildEngine string  `json:"build_engine"` // engine that runs the build
	Transfer    bool    `json:"transfer"`     // built on BuildEngine, then loaded into Engine
	NeedGB      float64 `json:"need_gb"`      // least memory to compile (0 when it doesn't compile)
	BuildGB     float64 `json:"build_gb"`     // memory of the building machine (0 = unknown)
	Note        string  `json:"note,omitempty"`
	Error       string  `json:"error,omitempty"` // the build would fail before starting
}

// EngineMemoryRead is one engine's memory as the preflight read it.
type EngineMemoryRead struct {
	Installed bool    `json:"installed"`
	Reachable bool    `json:"reachable"`
	GB        float64 `json:"mem_gb"` // 0 = unknown
	Error     string  `json:"error,omitempty"`
}

// compilesFor reports whether m's Dockerfile for compute compiles from source.
func (b *Builder) compilesFor(m catalog.Model, compute string) bool {
	data, err := os.ReadFile(filepath.Join(b.BaseDir, "docker", dockerfileName(modelFamily(m), compute)))
	return err == nil && compilesFromSource(data)
}

// readEngineMemories queries every installed engine's memory concurrently.
func readEngineMemories(ctx context.Context) (map[string]engineMem, map[string]EngineMemoryRead) {
	engines := []string{"docker", "podman"}
	mem := make(map[string]engineMem, len(engines))
	reads := make(map[string]EngineMemoryRead, len(engines))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, e := range engines {
		if _, ok := lookEngine(e); !ok {
			mem[e] = engineMem{Err: errNotInstalled}
			reads[e] = EngineMemoryRead{Error: "not installed"}
			continue
		}
		wg.Add(1)
		go func(e string) {
			defer wg.Done()
			gb, _, err := EngineMemory(ctx, e)
			r := EngineMemoryRead{Installed: true, Reachable: err == nil, GB: gb}
			if err != nil {
				r.Error = ClassifyEngineError(err)
				if r.Error == "" {
					r.Error = err.Error()
				}
			}
			mu.Lock()
			mem[e] = engineMem{GB: gb, Err: err}
			reads[e] = r
			mu.Unlock()
		}(e)
	}
	wg.Wait()
	return mem, reads
}

// Preflight decides every item from one memory read per engine. It never
// starts anything: the build still runs its own preflight when it starts, since
// memory can change in between.
func (b *Builder) Preflight(ctx context.Context, items []PreflightItem) ([]PreflightPlan, map[string]EngineMemoryRead) {
	mem, reads := readEngineMemories(ctx)
	plans := make([]PreflightPlan, 0, len(items))
	for _, it := range items {
		plans = append(plans, preflightOne(b, it, mem))
	}
	return plans, reads
}

func preflightOne(b *Builder, it PreflightItem, mem map[string]engineMem) PreflightPlan {
	p := PreflightPlan{Engine: it.Engine, Compute: it.Compute, BuildEngine: it.Engine}
	compute, err := b.resolveCompute(it.Model, it.Compute)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.Compute = compute
	p.Compiles = b.compilesFor(it.Model, compute)
	if !p.Compiles {
		// Nothing heavy to compile: it builds where it runs, so that engine
		// just has to answer.
		if t := mem[it.Engine]; t.Err != nil && (t.Err == errNotInstalled || transientEngineErr(t.Err)) {
			p.Error = engineDownText(it.Engine, t.Err)
		} else if t.known() {
			p.BuildGB = t.GB
		}
		return p
	}
	p.NeedGB = compileFloorGB(compute)
	if t := mem[it.Engine]; t.Err == errNotInstalled {
		p.Error = engineDownText(it.Engine, t.Err)
		return p
	}
	plan, err := decideBuild(it.Engine, compute, p.NeedGB, mem)
	p.BuildEngine, p.Transfer, p.BuildGB, p.Note = plan.Build, plan.Transfer, plan.BuildGB, plan.Note
	if err != nil {
		p.Error = err.Error()
		if transientEngineErr(err) {
			p.Error = engineDownText(it.Engine, err)
		}
	}
	return p
}

// engineDownText says an engine can't take the build, and what to do.
func engineDownText(engine string, err error) string {
	if err == errNotInstalled {
		return capitalize(engineLabel(engine)) + " isn't installed here, so it can't build this image. Pick the other engine."
	}
	if hint := ClassifyEngineError(err); hint != "" {
		return hint
	}
	return capitalize(engineLabel(engine)) + " isn't answering: " + err.Error()
}

type notInstalledError struct{}

func (notInstalledError) Error() string { return "engine not installed" }

var errNotInstalled error = notInstalledError{}
