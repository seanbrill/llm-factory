# local-llm

A local **LLM image factory**: configure what you want, and it spits out a
portable, self-contained container image to run elsewhere. From a small web GUI
you pick a model, name your image, choose an **engine** (Docker or Podman) and a
**compute target** (CPU, NVIDIA CUDA, or Vulkan/Apple-Metal GPU), and optionally
bake in an **initialization prompt** that specializes how the model behaves on
startup. The tool downloads the weights into `./models`, builds a `llama.cpp`
server image with the model baked in, exports it as a `.tar` into `./images`, and
lets you run and chat with it to verify. No Python, no Ollama.

> **The deliverable is the image.** For the full image spec and how other apps
> should talk to a running container, see **[docs/CONTAINER.md](docs/CONTAINER.md)**.

```
 Web GUI (Go)  ─►  download GGUF ─►  docker|podman build (CPU|CUDA|Vulkan) ─►  save .tar
 (localhost)        ./models/              model baked in                       ./images/
      │                                          │
      └─────────────── run + chat ──────────────┘  (OpenAI-compatible /v1/chat/completions)
```

### New here? The idea in plain words

You don't need to know Docker to use this. The short version:

- **A "model"** is the AI's brain file (a `.gguf`). The app downloads it for you.
- **An "image"** is a sealed box holding that model plus a tiny web server, so it
  runs the same on any computer.
- **An "engine"** (Docker or Podman) is the program that runs those boxes. You
  install **one** desktop app and the factory handles the rest.
- **CPU vs GPU:** CPU works on every machine but is slower; GPU is much faster if
  your computer has a supported one.

The whole flow: **install a desktop app → run one start script → open the web page →
click Build, then Run, then Test.**

## Get started (3 steps)

**1. Install a container engine.** Click-by-click instructions are in
[Installing Docker or Podman](#installing-docker-or-podman-desktop-apps) just below.
- **Docker Desktop** — easiest; covers CPU on every machine and NVIDIA GPUs.
  *Start here if you're unsure.*
- **Podman Desktop** — required for **GPU on a Mac**, and a free Docker Desktop
  alternative.

Make sure the app is **running** (its tray / menu-bar icon says "running") first.

**2. Start the factory.** From this project's folder:

```powershell
.\scripts\windows\start.ps1                 # Windows         (stop with .\scripts\windows\stop.ps1)
.\scripts\windows\start.ps1 -Port 9000      # optional: custom port
```
```bash
./scripts/macos/start.sh                  # macOS / Linux   (stop with ./scripts/macos/stop.sh)
PORT=9000 ./scripts/macos/start.sh        # optional: custom port
```

The first run builds a small helper image, then launches the factory. Leave it
running in that terminal.

**3. Open the web page and build a model.** Visit **http://localhost:8799**, then:
1. Pick a **model** — start with the ★ recommended one (small and fast).
2. Leave **Engine = Docker** and **Compute = CPU** (works on any machine), or see
   [Choosing an engine & compute](#choosing-an-engine--compute) to use a GPU.
3. Click **Build & export .tar** and watch the progress bar.
4. Under **Built images**, set a port and click **Run**.
5. Scroll to **Test**, type a prompt, and click **Send**.

That's the whole loop. Everything you download and build is saved under `./models`
and `./images` on your computer, so it persists between runs.

> **Under the hood:** the factory itself runs as a container and talks to your
> host's engine (the socket is mounted); `./models`, `./images`, and `./config` are
> shared back to your folders. See
> [docs/CONTAINER.md §10](docs/CONTAINER.md#10-running-the-factory-itself-in-docker).
>
> Prefer no Docker at all? Run the factory natively with `go run ./cmd/builder`
> (needs Go); flags: `-addr 127.0.0.1:9000`, `-open=false`, `-host …`,
> `-web internal/server/web`.

## Installing Docker or Podman (desktop apps)

You only need **one** engine to begin. Use **Docker** if you're unsure; add
**Podman** if you want GPU on a Mac. Both are free desktop apps with a one-time,
mostly click-through install. Works the same on Windows, macOS, and Linux.

### Option A — Docker Desktop (recommended for beginners)
1. Go to **https://www.docker.com/products/docker-desktop**, download the version
   for your OS, and run the installer. **Accept the defaults** (on Windows it sets
   up WSL2 for you). Reboot if it asks.
2. Open **Docker Desktop**. Wait until the whale icon (Windows tray / macOS menu
   bar) shows **"Engine running"** in green. That's all the setup CPU builds need.
3. *(Only for large models like the 14B)* Click the **gear ⚙ → Resources → Memory**
   and drag it to **at least 16 GB**, then **Apply & Restart**. Skipping this makes
   big models crash with `OOMKilled`.
4. Now run the start script from [Get started](#get-started-3-steps) — the factory
   finds Docker automatically.

> **For an NVIDIA GPU:** also install the NVIDIA driver and the **NVIDIA Container
> Toolkit** (on Windows that's inside WSL2), then choose **Compute = NVIDIA CUDA**
> in the UI. (Docker Desktop on a Mac is **CPU-only** — for Mac GPU use Podman, below.)

### Option B — Podman Desktop (free Docker alternative; needed for Mac GPU)
1. Go to **https://podman-desktop.io**, download, install, and launch it. It's
   open-source with no Docker Desktop subscription terms.
2. On first launch it checks whether **Podman** itself is installed. If it isn't,
   click **Install** and follow the prompts (it may ask for your password).
3. Create and start a **machine** — the small Linux VM Podman runs containers in:
   open **Settings → Resources → Podman → Create new…**, accept the defaults, then
   press **▶ Start**. The tile should say **Running**.
4. Run the start script — the factory **auto-detects** the running machine and turns
   on the **Podman** option in the Engine dropdown.

> **macOS GPU — one extra step.** GPU only works on a special **libkrun** machine,
> which the GUI may not let you choose. Create it once in a terminal (then manage it
> in Podman Desktop like any other machine):
> ```bash
> brew install podman krunkit              # skip if Podman Desktop already installed Podman
> podman machine init --provider libkrun
> podman machine start
> ```
> Then in the UI pick **Engine = Podman** and **Compute = GPU (Vulkan/Metal)**.
> (GPU under krunkit is experimental — see [Running on a Mac](#running-on-a-mac-apple-silicon).)

### Prefer the command line?
| OS | Docker | Podman |
|---|---|---|
| Windows | `winget install Docker.DockerDesktop` | `winget install RedHat.Podman` |
| macOS | `brew install --cask docker` | `brew install podman krunkit` |
| Linux | `apt`/`dnf` install `docker` (Docker Engine) | `apt`/`dnf` install `podman`, then `sudo systemctl enable --now podman.socket` |

On Linux, the `podman.socket` step lets the factory (which runs in a Docker
container) reach your host Podman; `start.sh` mounts it automatically when present.

## Choosing an engine & compute

The build form has two dropdowns. **Engine** is the container runtime (the desktop
app you installed); **Compute** is how the model runs — on the **CPU** (works
everywhere) or on a **GPU** (faster, if you have a supported one). When in doubt,
**Docker + CPU** runs anywhere. Otherwise pick the row that matches your machine:

| Your situation | Engine | Compute | What you need |
|---|---|---|---|
| Anything, anywhere (portable, slower) | Docker | **CPU** | nothing (and it's arm64-native on a Mac) |
| Windows / Linux with an **NVIDIA** GPU | Docker *or* Podman | **NVIDIA CUDA** | NVIDIA Container Toolkit (WSL2 on Windows) |
| **macOS** GPU (Apple Metal) | **Podman** | **GPU (Vulkan/Metal)** | a `krunkit`/libkrun Podman machine |
| Linux with an **AMD/Intel** GPU | Docker *or* Podman | **GPU (Vulkan/Metal)** | `/dev/dri` + Mesa Vulkan drivers |

Why Podman for the Mac GPU: Docker Desktop and Apple's own `container` tool can't
reach the Metal GPU (Apple Silicon GPUs have no IOMMU, so there's nothing to "pass
through"). A Podman **libkrun** machine paravirtualizes the GPU into the container
as a Vulkan device — the only way to GPU-accelerate a *container* on a Mac (~70–80%
of native). More in [Running on a Mac](#running-on-a-mac-apple-silicon) below.

> You don't have to choose up front: the factory image bundles **both** the
> `docker` and `podman` clients, and the start scripts mount whichever engines are
> present. Images and containers from either engine show up together (with an
> **Engine** column), and Run/Stop/Delete route back to the right one.

## Development (hot reload)

```powershell
.\scripts\windows\start-dev.ps1     # Windows   (./scripts/macos/start-dev.sh on macOS/Linux);  stop with .\scripts\windows\stop.ps1
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
- Use the regular `.\scripts\windows\start.ps1` for the production image (UI embedded, no watcher).

Then in the UI:
1. **Build** — choose a model, pick an **Engine** (Docker/Podman) and **Compute**
   (CPU / NVIDIA CUDA / GPU Vulkan-Metal), name the image, optionally add an
   **initialization prompt**, then *Build & export .tar*. A **progress bar** tracks
   download → compile → export (full log on demand). The build runs server-side, so
   it **survives a page refresh** — the UI reconnects to the in-progress build.
2. **Built images** — *Run* on a port, or *Delete* (also removes its `.tar`). The
   **Engine** column shows which runtime each image belongs to.
3. **Containers** — see what's running (across both engines); *Stop & remove*.
4. **Downloaded model files** — see cached weights and *Delete* ones you don't need.
5. **Test** — send a prompt to a running container and read the response.

Requires Docker (Desktop on Windows/macOS, Engine on Linux). The *Run* action picks
the GPU flags from the image automatically: `--gpus all` for **CUDA** (needs an
NVIDIA GPU + the NVIDIA Container Toolkit, via WSL2 on Windows) and `--device
/dev/dri` for **Vulkan** (needs a Podman libkrun machine on macOS, or `/dev/dri` on
Linux). See [Choosing an engine & compute](#choosing-an-engine--compute).

## Running on a Mac (Apple Silicon)
- **CPU container — supported and now native.** The factory builds a native
  **arm64** CPU image on Apple Silicon (NEON + dotprod), not x86 under emulation.
  Pick **CPU** in the UI as usual. For a 14B model raise Docker Desktop's memory
  limit (Settings → Resources) to ≥16 GB or the container is `OOMKilled`.
- **GPU container — not via Docker Desktop or Apple's `container` tool.** Neither
  can reach the Metal GPU: Apple Silicon GPUs have no IOMMU (so no PCI
  passthrough), and Apple's own containerization maintainers confirm GPU access is
  unsupported with no roadmap. The **CUDA image is NVIDIA-only**. *Direct* Metal
  passthrough into a Linux container is genuinely impossible.
- **GPU container — possible via Podman + libkrun (krunkit), and the factory builds
  it for you.** The community route around the passthrough wall is
  paravirtualization: a `virtio-gpu` device plus a Vulkan→Metal (MoltenVK) bridge,
  run under `krunkit`. Pick **Engine = Podman, Compute = GPU (Vulkan/Metal)** in the
  UI and the factory builds the Vulkan image ([docker/Dockerfile.vulkan](docker/Dockerfile.vulkan))
  and runs it on your libkrun machine. A Vulkan build gets ~70–80% of native.
  *Experimental:* GPU under krunkit needs a Mesa build with the virtio/Venus Vulkan
  driver; if the container sees no GPU, compare against
  [RamaLama](https://github.com/containers/ramalama) and Red Hat's
  [native-speed write-up](https://developers.redhat.com/articles/2025/09/18/reach-native-speed-macos-llamacpp-container-inference).
- **Simplest Mac GPU path — run the engine natively** (no container) against the
  GGUF already in `./models`:
  ```bash
  brew install llama.cpp
  llama-server -m models/Qwen2.5-14B-Instruct-Q4_K_M.gguf -ngl 99 -c 8192 --port 8080
  ```
  This uses Metal (`-ngl 99` = all layers on GPU) and exposes the **same**
  OpenAI-compatible API on `:8080` the rest of this project expects.

## Where things land
| Path | What |
|---|---|
| `config/models.json` | The model catalog — **edit this to add/remove models** (seeded on first run). |
| `models/` | Downloaded GGUF weight files (one copy per model, reused across builds). |
| `images/` | Exported image tarballs (`docker`/`podman save`), one `.tar` per build. |
| `docker/Dockerfile.cpu` / `.cuda` / `.vulkan` | The image recipes (editable). |

## Project layout
```
cmd/builder/         entrypoint: starts the local web UI (the factory)
cmd/llmgate/         in-container shim: supervises llama-server + injects the
                     baked init prompt (compiled inside the image at build time)
internal/catalog/    config-driven model list (+ embedded default)
internal/builder/    download / build / save / run / stop / list (docker|podman)
internal/server/     HTTP API + embedded vanilla-JS UI (internal/server/web)
internal/llm/        OpenAI-compatible client (generic Chat + typed example)
docker/              CPU, CUDA, and Vulkan Dockerfiles (bake model.gguf + prompt + gate)
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
- **Engine:** one `llama.cpp` server binary covers CPU (x86 AVX2/FMA or, on
  Apple Silicon, arm64 NEON+dotprod), NVIDIA (CUDA), and **Vulkan** (cross-vendor
  GPU, and the only GPU backend that works in a *container* on a Mac, via a Podman
  libkrun machine). All speak the OpenAI API, so the same Go client works
  everywhere. The CPU Dockerfile selects SIMD by the build's `TARGETARCH`, so on a
  Mac it builds a native arm64 image.
- **Container engine:** the factory shells out to **`docker` or `podman`** (chosen
  per build in the UI; `docker` is the default). Podman is what unlocks the macOS
  GPU path; its CLI is otherwise docker-compatible for build/run/save/ps/inspect.
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
- **Zero third-party Go deps:** stdlib only — shells out to the `docker`/`podman` CLI.

## Tunables
Set as `docker run -e ...` (or `podman run -e ...`) overrides (defaults in the
Dockerfiles): `PORT`, `CTX_SIZE`, `THREADS` (match host vCPUs), `PARALLEL`, and
`NGL` (GPU layers — CUDA and Vulkan images, default 99).

## Optional: cloud deploy
`deploy/ecs-task-definition.json` runs a built image on AWS Fargate (2 vCPU /
4 GB baseline; bump memory to 8 GB if you raise context or hit `OOMKilled`).
Push your image to ECR, fill in the placeholder account/region/role ARNs, then:
```bash
aws ecs register-task-definition --cli-input-json file://deploy/ecs-task-definition.json
```
