# Architecture overview

This explains what the factory is made of and how a build and a run actually flow
through it. It's written so you can read the code afterward and know where you are.

## The big picture

The factory is a small **Go web server** (the "control panel" at `localhost:8799`)
that doesn't do AI itself — it **orchestrates** the container engine (`docker` or
`podman`) by running the same commands you'd type by hand. It has **zero external
Go dependencies** (standard library only) on purpose: it's small and auditable.

```
┌─────────────────────────── your machine ───────────────────────────┐
│                                                                     │
│  Browser ──HTTP──►  Factory control panel (Go, :8799)               │
│                        │   shells out to ▼                          │
│                     docker / podman  ──►  builds & runs containers   │
│                        │                                             │
│                        ├─►  Model container  (llama-server + llmgate)│
│                        │      serves an OpenAI API on a local port   │
│                        │                                             │
│                        └─►  Caddy proxy container                    │
│                               routes  http://<name>.localhost  ──────┼──► model
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

## The components (and where they live)

| Piece | What it does | Code |
|---|---|---|
| **Control panel server** | Serves the web UI + a small JSON API (`/api/...`). | `internal/server/server.go` |
| **Web UI** | The single-page interface (hand-written, no framework). | `internal/server/web/{index.html,app.js,style.css}` |
| **Builder** | The brain: downloads models, assembles a build context, runs `docker/podman build`, runs/stops containers, manages the proxy. | `internal/builder/builder.go` |
| **Catalog** | The list of available models (seeded from an embedded JSON). | `internal/catalog/` |
| **llmgate** | A tiny shim baked **inside** each llama.cpp model image. It launches `llama-server` and injects a baked "system prompt." | `cmd/llmgate/main.go` |
| **Dockerfiles** | One per runtime+compute: how to build each kind of model image. | `docker/Dockerfile.*` |
| **Entrypoint** | Where the program starts. | `cmd/builder/main.go` |

## Where the code starts (entrypoint)

`cmd/builder/main.go` → `main()`:
1. Loads the **catalog** (`catalog.Load`).
2. Creates the **builder** (`builder.New`) — resolves the `models/`, `images/`,
   `config/` folders.
3. Creates the **server** (`server.New`) and starts listening on `:8799`.
4. Kicks off **reboot recovery** in the background (`builder.RecoverOnStartup`) —
   restarts autostart models and rebuilds the proxy after a computer restart.

## How a BUILD flows

When you click "Build", the UI POSTs to `/api/build`. Then in `builder.Build`:

1. **Pick the runtime family** from the model's *modality*
   (`runtimeFamily`): chat/code/vision/embeddings → **llama.cpp**; image →
   **stable-diffusion.cpp**; speech-to-text → **whisper.cpp**; text-to-speech →
   **Piper**. Each family has its own `docker/Dockerfile.<family>.<compute>`.
2. **Download the model** if needed (`EnsureModel`) into `models/`. Vision models
   also download a second file, the *projector* (`EnsureMMProj`).
3. **Assemble a tiny build context**: a temp folder with the chosen Dockerfile and a
   *hardlink* to the model (so multi-GB weights aren't copied). llama.cpp models also
   stage the system prompt, the projector, and the `llmgate` source.
4. **Run `<engine> build`** with the right build-args (context size, modality, …).
   Progress streams to the UI's build log.
5. **Garbage-collect** the previous dangling image so disk doesn't grow per rebuild.

The result is a single self-contained image like `local-llm/qwen3-4b:latest`.

## How a RUN flows

When you click "Run", the UI POSTs to `/api/run`. In `builder.Run`:

1. Read the image's baked **labels** (engine, compute, route, autostart…) so it runs
   the way it was built — a CUDA image gets `--gpus all`, a Vulkan image gets
   `--device /dev/dri`, etc.
2. Start the container, publishing its port on **`127.0.0.1`** only (so it's not
   exposed to your local network).
3. **Regenerate the Caddy proxy** (`RegenProxy`) so `http://<route>.localhost` points
   at the running container.

Inside an llama.cpp image, **llmgate** (PID 1) starts `llama-server`, then sits in
front of it as a reverse proxy that injects the baked system prompt into chat
requests. Everything else passes through, so the container is a drop-in
OpenAI-compatible server.

## The reliability bits (why it's robust)

- **Flaky-socket retry:** on macOS the factory (in Docker) talks to Podman through a
  bind-mounted socket that occasionally drops. Engine commands retry transient
  "connection refused" errors instead of failing.
- **Friendly errors:** raw engine failures (`exit status 125`) are translated into
  actionable messages ("engine unreachable — restart it", "port in use", …).
- **Health endpoints:** `/healthz` (process up) and `/readyz` (engines reachable).
- **Reboot recovery:** after a restart, autostart models come back and the proxy is
  rebuilt automatically.

## Folder layout

```
cmd/builder/      the control-panel program (entrypoint)
cmd/llmgate/      the in-image shim baked into llama.cpp models
internal/builder/ build/run/proxy logic (the core)
internal/server/  HTTP API + embedded web UI
internal/catalog/ the model list (+ embedded default seed)
docker/           one Dockerfile per runtime + compute target
scripts/          start/stop scripts, organized by OS
docs/             you are here
models/ images/   downloaded weights and exported image tarballs (git-ignored)
config/           runtime catalog + generated proxy config (git-ignored)
```
</content>
