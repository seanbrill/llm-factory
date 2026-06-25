# local-llm

A local **LLM image factory**: configure what you want, and it spits out a
portable, self-contained Docker image to run elsewhere. From a small web GUI you
pick a model, name your image, choose CPU or NVIDIA-GPU, and optionally bake in
an **initialization prompt** that specializes how the model behaves on startup.
The tool downloads the weights into `./models`, builds a `llama.cpp` server image
with the model baked in, exports it as a `.tar` into `./images`, and lets you run
and chat with it to verify. No Python, no Ollama.

> **The deliverable is the image.** For the full image spec and how other apps
> should talk to a running container, see **[docs/CONTAINER.md](docs/CONTAINER.md)**.

```
 Web GUI (Go)  ──►  download GGUF ──►  docker build (CPU|CUDA) ──►  docker save .tar
 (localhost)         ./models/             model baked in              ./images/
      │                                          │
      └─────────────── run + chat ──────────────┘  (OpenAI-compatible /v1/chat/completions)
```

## Quick start

The factory runs **in Docker** — cross-platform, no Go toolchain needed on the
host. Just Docker (Desktop on Windows/macOS, Engine on Linux):

```powershell
.\start.ps1                 # Windows   — build + run, UI at http://localhost:8799
.\stop.ps1                  #           — stop + remove it
```
```bash
./start.sh                  # macOS / Linux        ./stop.sh   to stop
PORT=9000 ./start.sh        # custom port (PowerShell: .\start.ps1 -Port 9000)
```

The factory container builds/runs model images through the **host's Docker
daemon** (the socket is mounted), and bind-mounts `./models`, `./images`,
`./config` back to your host so downloads, tarballs, and the catalog persist.
See [docs/CONTAINER.md §10](docs/CONTAINER.md#10-running-the-factory-itself-in-docker).

> For native development without Docker you can still run the factory directly
> with `go run ./cmd/builder` (needs Go); flags like `-addr 127.0.0.1:9000`,
> `-open=false`, `-host …`, `-web internal/server/web` are available there.

## Development (hot reload)

```powershell
.\start-dev.ps1     # Windows   (./start-dev.sh on macOS/Linux);  stop with .\stop.ps1
```

Runs the factory in a dev container with the repo bind-mounted and **UI and
backend reloading independently**:

- **UI** is served from disk (`-web`), so editing `internal/server/web/*` just
  needs a **browser refresh** — no rebuild, and the backend is *not* restarted.
- **Backend** is run under [`wgo`](https://github.com/bokwoon95/wgo), which
  watches **`*.go` only** and rebuilds/restarts the server on change — the UI is
  untouched. Watch it with `docker logs -f local-llm-factory-dev`.

Notes:
- The watcher uses **polling** (`-poll`) because inotify file events don't cross
  Docker Desktop bind mounts on Windows/macOS — plain event-watching wouldn't
  see your host edits.
- The Go build cache is kept in a named volume so rebuilds are fast (this project
  has zero third-party deps, so a backend reload is ~1–2s).
- Use the regular `.\start.ps1` for the production image (UI embedded, no watcher).

Then in the UI:
1. **Build** — choose a model, name the image, pick **CPU** or **NVIDIA GPU (CUDA)**,
   optionally add an **initialization prompt**, then *Build & export .tar*. A
   **progress bar** tracks download → compile → export (full log on demand). The
   build runs server-side, so it **survives a page refresh** — the UI reconnects
   to the in-progress build and keeps showing its log/progress.
2. **Built images** — *Run* on a port, or *Delete* (also removes its `.tar`).
3. **Containers** — see what's running; *Stop & remove* to tear down.
4. **Downloaded model files** — see cached weights and *Delete* ones you don't need.
5. **Test** — send a prompt to a running container and read the response.

Requires Docker (Desktop on Windows). GPU builds also need an NVIDIA GPU + the
NVIDIA Container Toolkit (via WSL2 on Windows); the *Run* action adds `--gpus all`
automatically for CUDA images.

## Where things land
| Path | What |
|---|---|
| `config/models.json` | The model catalog — **edit this to add/remove models** (seeded on first run). |
| `models/` | Downloaded GGUF weight files (one copy per model, reused across builds). |
| `images/` | Exported image tarballs (`docker save`), one `.tar` per build. |
| `docker/Dockerfile.cpu` / `Dockerfile.cuda` | The image recipes (editable). |

## Project layout
```
cmd/builder/         entrypoint: starts the local web UI (the factory)
cmd/llmgate/         in-container shim: supervises llama-server + injects the
                     baked init prompt (compiled inside the image at build time)
internal/catalog/    config-driven model list (+ embedded default)
internal/builder/    download / docker build / save / run / stop / list
internal/server/     HTTP API + embedded vanilla-JS UI (internal/server/web)
internal/llm/        OpenAI-compatible client (generic Chat + typed example)
docker/              CPU and CUDA Dockerfiles (bake model.gguf + prompt + gate)
Dockerfile.factory   runs the factory ITSELF in a container (Docker-out-of-Docker)
Dockerfile.dev       dev image: wgo (Go watcher) + UI served from disk
start.* / stop.*     build + run / stop the factory container (PowerShell + bash)
start-dev.*          run the factory with UI + backend hot-reload
docs/CONTAINER.md    image spec + how other apps interact with the container
deploy/              OPTIONAL ECS/Fargate task definition for later cloud deploy
```

## Curated models
Hand-picked GGUF (Q4_K_M) models that run well locally, from tiny CPU-friendly to
GPU-class. Edit `config/models.json` to change the list.

| Tier | Model | ~Size | Notes |
|---|---|---|---|
| tiny | Llama 3.2 1B, Qwen2.5 1.5B | 0.8–1.0 GB | Fast on any CPU |
| small | **Qwen2.5 3B** ★, Llama 3.2 3B, Phi-3.5-mini | 2.0–2.4 GB | CPU sweet spot |
| mid | Mistral 7B v0.3, Qwen2.5 7B, Llama 3.1 8B | 4.4–4.9 GB | GPU or beefy CPU |
| large | Qwen2.5 14B | ~9.0 GB | Wants ≥12 GB VRAM |

> Catalog URLs point at public HuggingFace GGUF repos. If a download 404s,
> the repo/file name may have changed — fix the `url` in `config/models.json`.

## How it works / design notes
- **Engine:** one `llama.cpp` server binary covers CPU (AVX2/FMA) and NVIDIA
  (CUDA) and speaks the OpenAI API, so the same Go client works everywhere.
- **Initialization prompt:** `llama.cpp` has no default-system-prompt flag, so
  each image runs a tiny static Go shim (`llmgate`, PID 1) in front of
  `llama-server` that injects the baked prompt into `/v1/chat/completions` — as a
  default (if the client sends none) or always-enforced. Empty prompt = plain
  passthrough. See [docs/CONTAINER.md](docs/CONTAINER.md#4-the-baked-initialization-prompt).
- **Build context stays tiny:** the selected model is hardlinked from `./models`
  into a temp context as `model.gguf`; the Dockerfile `COPY`s that. No multi-GB
  re-copy, and other models aren't sent to the daemon.
- **Static link:** `BUILD_SHARED_LIBS=OFF` folds llama.cpp's libs into the
  `llama-server` binary, so the runtime image only needs `libgomp1` (+ CUDA
  runtime libs for the GPU image).
- **Zero third-party Go deps:** stdlib only — shells out to the `docker` CLI.

## Tunables
Set as `docker run -e ...` overrides (defaults in the Dockerfiles):
`PORT`, `CTX_SIZE`, `THREADS` (match host vCPUs), `PARALLEL`, and `NGL`
(GPU layers, CUDA image only).

## Optional: cloud deploy
`deploy/ecs-task-definition.json` runs a built image on AWS Fargate (2 vCPU /
4 GB baseline; bump memory to 8 GB if you raise context or hit `OOMKilled`).
Push your image to ECR, fill in the placeholder account/region/role ARNs, then:
```bash
aws ecs register-task-definition --cli-input-json file://deploy/ecs-task-definition.json
```
