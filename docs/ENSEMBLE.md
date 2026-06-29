# Ensemble — multimodal "super model" design

This document is the **north-star spec** for the next phase of the factory: turning
a set of single-purpose model images into one multimodal unit called an
**Ensemble** — a tiny orchestrator (the **Conductor**) plus a roster of specialist
models, presented to clients as a single OpenAI-compatible endpoint.

It captures decisions already made so we build against a fixed target. It is a
plan, not yet an implementation.

---

## 1. What an Ensemble is

> No single open, locally-runnable model covers chat **and** vision **and** speech
> **and** image/video generation — image/video are diffusion models and speech is
> a separate architecture. So instead of one omni-model, an Ensemble combines
> specialists behind one front door.

A built Ensemble is the same kind of deliverable the factory already produces: a
runnable image (plus `.tar` export) that speaks the OpenAI HTTP API. The
difference is what's inside:

```
                 ┌──────────────── OpenAI-compatible front door ───────────────┐
   client  ───▶  │  Conductor  (tiny router model, 0.6B–4B)                     │
                 │    • detects input modality (image? audio? "draw…"? "say…")   │
                 │    • picks tool(s) via function-calling                       │
                 │    • stitches multi-step chains, returns one response         │
                 └───────────────┬──────────────────────────────────────────────┘
                                 │  ensureUp("image-gen") / route / ensureDown
                    ┌────────────▼──────────────┐
                    │  Manager  (VRAM-budgeted)  │   keeps a warm set; lazy-loads
                    │  warm pool + LRU/TTL evict │   and evicts the rest on demand
                    └─┬───┬────┬─────┬─────┬─────┘
              chat-LLM  VLM  Whisper Piper SDXL   …video (gen + understanding)
```

The **Conductor does not answer** — it classifies intent, calls the right
specialist(s), and assembles the result. Example chain: *audio in → `transcribe`
(Whisper) → `chat` (LLM) → `speak` (Piper) → audio out.*

---

## 2. Goals / non-goals

**Goals**
- One endpoint that transparently handles chat, vision, STT, TTS, image gen,
  video gen, and video understanding.
- Specialists **spin up and down on the fly** so only what's needed occupies VRAM.
- Built on the existing factory primitives (per-modality images, run/stop).
- A **drop-in harness**: register a model + the tool it backs in a manifest; the
  Conductor picks it up. No code change to add a capability.

**Non-goals (for now)**
- Training/fine-tuning models. We only compose existing ones.
- Multi-host / clustering. Single machine, single engine.
- Guaranteed concurrency across all modalities at once (VRAM-bound — see §6).

---

## 3. Two package modes (one Conductor, two Manager backends)

The factory stays "dynamic enough" for both by hiding lifecycle behind a single
**Manager interface** — `ensureUp(id)` / `ensureDown(id)` / `route(id, req)` —
implemented two ways, chosen at build time:

| Mode | Image contents | Lifecycle backend | When |
|---|---|---|---|
| **Embedded (mega)** *(default)* | Conductor + every selected runtime + all weights | supervises specialist **processes** inside the one container | a single portable artifact, fixed roster |
| **Orchestrated (bundle)** | Conductor + Manager + manifest only | starts/stops **sibling containers** via the host engine (same Docker-out-of-Docker the factory uses today) | small image, swap specialists without rebuild |

The Conductor's logic, tool-calling, and request flow are identical in both modes
— only the Manager backend differs. The Ensemble builder exposes a **Package
mode** toggle.

Trade-offs: Embedded is large (tens of GB, all weights on disk) but truly
self-contained; Orchestrated is small and flexible but needs the host Docker
socket and the specialist images present on the host.

---

## 4. The Conductor (tiny router)

- A small, fast model (target **0.6B–4B**, e.g. `qwen3-4b`) whose only job is
  **intent → tool-call**. It never composes long answers.
- Two routing strategies, evolving:
  1. **Heuristic** (P1): branch on concrete input signals — an image part → VLM;
     an audio part → Whisper; text like *"generate/draw an image"* → SDXL; etc.
  2. **Tool-calling LLM** (P3): the Conductor is given the tool registry and emits
     real function calls, enabling multi-step chains and ambiguous routing.
- Sees only enough to route (modality + a short intent), then hands off. Keeps it
  cheap and always-warm.

---

## 5. Tool registry / manifest (the harness)

A declarative manifest maps **tool → specialist**, and is the entire "drop a model
in and give it tools" mechanism:

```jsonc
{
  "tools": [
    { "name": "chat",           "model": "qwen3-8b",        "modality": "text",   "schema": {…} },
    { "name": "see_image",      "model": "qwen2.5-vl-7b",   "modality": "vision", "schema": {…} },
    { "name": "transcribe",     "model": "whisper-large-v3","modality": "audio-stt" },
    { "name": "speak",          "model": "piper-en-us",     "modality": "tts" },
    { "name": "generate_image", "model": "sdxl-turbo",      "modality": "image" },
    { "name": "generate_video", "model": "<tbd>",           "modality": "video" },
    { "name": "watch_video",    "model": "qwen2.5-vl-7b",   "modality": "vision", "via": "frame-shim" }
  ]
}
```

Adding a capability = adding a row + ensuring the backing image exists. The
Conductor's tool list and the Manager's roster both derive from this manifest.

---

## 6. VRAM budget + eviction (the hard constraint)

On a 12 GB GPU you **cannot** run every specialist at once. The Manager enforces a
budget:

- **Warm set**: keep the Conductor (+ usually the chat LLM) resident.
- **Lazy-load** any other specialist on first use; **evict** by **LRU/TTL** when
  the budget is exceeded or after an idle timeout.
- Each manifest entry carries an estimated VRAM cost (reuse the catalog's
  `min_vram_gb`). The Manager admits/evicts to stay under budget.
- Trade-off surfaced to the user: cold-start latency (a few seconds to load a
  model) vs. memory. Frequently-paired specialists can be pinned warm.

---

## 7. Modalities & specialists

| Capability | Tool | Specialist (today) | Runtime |
|---|---|---|---|
| Chat / code / reasoning | `chat` | catalog text models | llama.cpp |
| Image understanding | `see_image` | Qwen2.5-VL, Gemma-3-vision | llama.cpp + mmproj |
| Speech → text | `transcribe` | Whisper | whisper.cpp |
| Text → speech | `speak` | Piper | piper |
| Image generation | `generate_image` | SDXL / SD | stable-diffusion.cpp (sd-server) |
| **Video generation** | `generate_video` | Wan 2.2 / LTX (new) | stable-diffusion.cpp (same runtime — see §9) |
| **Video understanding** | `watch_video` | any VLM + frame shim | *see §8* |

---

## 8. Video understanding — the frame-sampling shim

Native video VLMs exist (Qwen2.5-VL takes video; MiniCPM-V, LLaVA-Video), and they
all internally **sample frames and feed them as an image sequence**. We do the same
in an **llmgate-style shim**, so *any* VLM becomes video-capable and we control the
sampling:

1. Intercept a video input on the way to the VLM.
2. **ffmpeg** decodes the video.
3. **Smart-sample** frames instead of all of them:
   - **Uniform** — ~1 fps or N evenly-spaced frames; or
   - **Adaptive keyframes** — keep frames where the scene actually changes
     (frame-diff / SSIM / perceptual-hash above a threshold), drop near-duplicates.
   - **Cap at K** (e.g. 8–16) to fit the VLM's context; optionally tag each frame
     with its timestamp.
4. Send as a single multi-image message: *"ordered frames from a video…"*.

This reuses the exact injection pattern of the system-prompt shim and isn't tied
to any one model.

---

## 9. Video generation — reuses the existing diffusion runtime

**Key finding (June 2026):** `stable-diffusion.cpp` — the *same* `sd-server` the
factory already uses for SDXL image gen — now supports **video** models
(**Wan 2.1 / 2.2**, **LTX-2.3**) via **GGUF** weights on the ggml backend. So video
gen needs **no ComfyUI / no Python** — it stays in the zero-Python C++/GGUF design.

**Verified build shape (from the sd.cpp docs):**

- Video runs through the **`sd-cli` binary, not `sd-server`** (which is image-only
  today). So the `video` runtime invokes the CLI per request and returns the file
  — a new entrypoint, not the existing HTTP image server.
- Wan 2.2 is **multi-file** — it needs **four** weights: high-noise + low-noise
  diffusion GGUFs, a VAE (`wan_2.1_vae.safetensors`), and a umt5 T5 encoder GGUF.
  → **the catalog schema must grow** beyond single-file (+ mmproj) to a list of
  weight files per model, and the downloader/Dockerfile must fetch/bake all of them.
- Exact T2V invocation (Q8_0 shown; use Q4 + offload for 12 GB):
  ```
  sd-cli -M vid_gen \
    --diffusion-model Wan2.2-T2V-A14B-LowNoise-Q4_K_M.gguf \
    --high-noise-diffusion-model Wan2.2-T2V-A14B-HighNoise-Q4_K_M.gguf \
    --vae wan_2.1_vae.safetensors --t5xxl umt5-xxl-encoder-Q4_K_M.gguf \
    -p "<prompt>" -W 832 -H 480 --video-frames 33 \
    --steps 10 --cfg-scale 3.5 --sampling-method euler --flow-shift 3.0 \
    --diffusion-fa --offload-to-cpu -o out.<fmt>
  ```
- Pieces: new **`video`** modality, multi-file catalog support, a CLI-driven video
  runtime image, a `/api/video/generate` endpoint, and a chat `video` modality.

**Reality check:** this is a *real* new workstream (CLI runtime + multi-file
catalog + ~15 GB of Wan weights), and it can only be smoke-tested on the GPU box.
Build it as a focused effort with a generation test on the 4070 — not blind.

**Models for 12 GB** (verify quant/VRAM at build time):
- **Wan 2.2** — quality pick; 14B at **Q4_K_M GGUF + T5 CPU-offload ≈ 6–8 GB @ 480p**;
  lighter **5B TI2V (8–12 GB)** and **1.3B (4–6 GB)** variants.
- **LTX-Video / LTX-2** — fast/efficient; LTX-2.3 also does audio+video in one pass.
- **CogVideoX-2B** — lightweight fallback.
- Skip **HunyuanVideo / Mochi-1** (need ~24 GB).

---

## 10. The Ensemble builder (UI)

A dedicated **Ensembles** page in the factory:

- Pulls from **existing built images** (the specialists you've already made).
- Pick a **Conductor** (tiny model) + check which specialists/tools to include.
- **Package mode** toggle (Embedded / Orchestrated) + VRAM budget + warm-set pins.
- Builds and exports the Ensemble like any other image (`.tar`).
- "Conductor" is the orchestrator; the bundle is the "Ensemble".

---

## 11. Phased roadmap

| Phase | Deliverable | Status |
|---|---|---|
| **P1** | Conductor **router MVP** — one OpenAI endpoint dispatching by input type. | ✅ built (heuristic classify in `ensemblegate`) |
| **P2** | **Manager** — on-demand `ensureUp`/`ensureDown` + VRAM budget + LRU eviction. | ✅ built (orchestrated mode) |
| **P3** | **Tool-calling Conductor** — model-driven routing; chains. | ◑ tool-calling hook built (heuristic fallback); multi-step chains TODO |
| **P4** | **Ensembles builder page** — compose from images, package-mode toggle. | ✅ built |
| **P5** | **Export** the Ensemble as a runnable artifact (`.tar`). | ✅ built (reuses image export) |
| **Video gen** | `video` modality + multi-file catalog + sd-cli runtime. | ✅ built (compile-verified; GPU smoke-test pending) |
| **Video understanding** | ffmpeg frame-sampling shim. | ⬜ not started |
| **Embedded (mega)** | bake all runtimes into one image. | ⬜ data model + toggle present; runtime is orchestrated only |

---

## 12. Open questions / risks

- **Conductor reliability**: tiny models route adequately with heuristics; true
  tool-calling may need a 4B+ that follows a tool schema reliably.
- **Embedded image size**: a full-modality mega image is tens of GB. Acceptable as
  the user's explicit default, but document the cost.
- **Cold-start UX**: lazy-loading adds latency on first use of a modality; needs a
  "warming…" signal in responses.
- **Video gen at 12 GB**: likely the most constrained piece; may ship as a
  best-effort, low-res capability first.

---

## 13. Implementation status (as built — compile-verified, GPU smoke-test pending)

Everything below compiles (`go build ./...`, `node --check`) but has **not** been
run on a GPU. Treat the GPU-gated items as drafts to smoke-test, not finished.

**Video generation**
- Catalog multi-file schema: `ExtraFiles []WeightFile` in `internal/catalog/catalog.go`.
- Downloader + build-context staging + manifest: `internal/builder/builder.go`
  (`EnsureExtraFiles`, the `video` family block, `stageVideogate`).
- Runtime: `cmd/videogate/main.go` (HTTP wrapper round the `sd` CLI),
  `docker/Dockerfile.video.cuda` + `.cpu`.
- Endpoint: `/api/video/generate` (`internal/server/server.go`).
- UI: a `video` modality in chat (icon, `genVideo`, `<video>` bubble, settings).
- Catalog entry: `wan2.2-t2v-a14b` (both catalog JSONs).
- **Gates to verify on GPU:** the four weight URLs (VAE/T5 are best-guess), the
  `SDCPP_REF` tag (must include `vid_gen`), `-DSD_CUDA=ON`, the `sd`/`sd-cli`
  binary name (auto-detected), and the output container (auto-detected). Tune the
  CLI live via the `EXTRA_ARGS` container env without a rebuild.

**Ensemble (Conductor)**
- Shared model + store: `internal/ensemble/ensemble.go` (config/ensembles.json).
- Conductor runtime: `cmd/ensemblegate/main.go` — heuristic routing, optional
  model tool-calling, VRAM-budgeted LRU lifecycle (orchestrated: `docker run/rm`
  sibling specialists), OpenAI passthrough for chat/vision, media-as-data-URL for
  image/video/tts.
- Build: `Builder.BuildEnsemble` + `docker/Dockerfile.ensemble` (Conductor +
  docker CLI + baked manifest). Run mounts the docker socket for ensemble images.
- API: `/api/ensembles` (CRUD), `/api/ensembles/delete`, `/api/ensemble/build`.
- UI: the **Ensembles** page (compose from built images, package-mode/routing/VRAM,
  build into an image with a live log).
- **Gates to verify on GPU:** the Conductor reaching specialists via
  `host.docker.internal`, the docker-in-docker socket permissions, health-probe
  paths per specialist (`/health`), and media return semantics for external clients.

**Known TODO (not yet built):** embedded/mega runtime (only orchestrated runs),
multi-step tool chains (e.g. STT→chat→TTS), the video-understanding frame shim,
and warm-set pinning in the Manager.
