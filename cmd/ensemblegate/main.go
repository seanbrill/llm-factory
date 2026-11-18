// Command ensemblegate is the Conductor: the entrypoint of an Ensemble image.
//
// It presents one OpenAI-compatible model in front of the ensemble's members
// (gate.go) and, for each request:
//  1. decides which member answers (router.go: the heuristic, or a small
//     conductor model; routing is one replaceable piece);
//  2. makes that member ready (manager.go): starts it as a sibling container
//     through the host engine, waits for its health check, and evicts the least
//     recently used idle members to stay within the memory budget;
//  3. calls it with its runtime's real request shape (members.go) and answers
//     in OpenAI's shape, with generated media and speech in the reply.
//
// The persona and voice are pushed by the factory at run time (persona.go),
// never baked in. Stdlib only, plus the engine CLI (docker). See
// docs/ENSEMBLE.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/ensemble"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadManifest(path string) (ensemble.Manifest, error) {
	var mani ensemble.Manifest
	data, err := os.ReadFile(path)
	if err != nil {
		return mani, err
	}
	if err := json.Unmarshal(data, &mani); err != nil {
		return mani, err
	}
	// MODEL_HOST overrides where member ports are reached (e.g. the docker
	// bridge's gateway on a Linux host).
	mani.ModelHost = env("MODEL_HOST", mani.ModelHost)
	if mani.ModelHost == "" {
		mani.ModelHost = ensemble.DefaultModelHost
	}
	if mani.VRAMBudgetGB <= 0 {
		mani.VRAMBudgetGB = 12
	}
	if mani.ID == "" {
		mani.ID = ensemble.Slug(mani.Name)
	}
	return mani, nil
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("[ensemblegate] ")
	mani, err := loadManifest(env("MANIFEST", "/ensemble/manifest.json"))
	if err != nil {
		log.Fatalf("read the manifest: %v", err)
	}
	if mani.PackageMode == "embedded" {
		log.Printf("embedded packaging isn't built yet: running orchestrated (members start as containers)")
	}
	// The baked harness (built-in tools for the brain): the same file and
	// HARNESS_* overrides as llmgate. Empty or missing: member tools only.
	var harness *agent.Config
	if cfg, ok, err := agent.Load(env("HARNESS_FILE", "/etc/ensemblegate/harness.json"), os.Getenv); err != nil {
		log.Printf("harness: %v; the brain gets the member tools only", err)
	} else if ok {
		harness = &cfg
	}
	if harness == nil {
		// No baked harness: the brain can still get MCP servers (HARNESS_MCP,
		// the admin API, /mcp chat commands).
		if cfg, err := (agent.Config{Tools: []string{}}).WithEnv(os.Getenv); err != nil {
			log.Printf("harness: %v", err)
		} else {
			harness = &cfg
		}
	}
	eng := cliEngine{bin: env("ENGINE", "docker")}
	g := newGate(mani, eng, harness, log.Printf)
	g.mg.weightsDir = strings.TrimSpace(os.Getenv("WEIGHTS_DIR"))
	g.persona.envPrompt = strings.TrimSpace(os.Getenv("SYSTEM_PROMPT"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		g.mg.Adopt(ctx)
		g.mg.WarmPinned(ctx)
	}()
	go g.mg.Monitor(ctx, 15*time.Second)

	port := env("PORT", "8080")
	srv := &http.Server{Addr: ":" + port, Handler: agent.NewHostGuard(g.Handler(), nil), ReadHeaderTimeout: 30 * time.Second}
	router, note, tools := g.routerStatus()
	log.Printf("ensemble %q (%s) on :%s: %d members, %s routing, %.0f GB budget, members reached at %s",
		mani.Name, mani.ID, port, len(g.mg.order), router, mani.VRAMBudgetGB, mani.ModelHost)
	if note != "" {
		log.Printf("routing: %s", note)
	}
	if len(tools) > 0 {
		log.Printf("the brain's tools: %s", strings.Join(tools, ", "))
	}
	go func() {
		<-ctx.Done()
		// A graceful stop (docker stop) frees the members' memory too. The
		// factory's Stop removes them itself, since it doesn't wait for this.
		log.Printf("stopping: removing members")
		sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		g.mg.StopAll(sctx)
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
