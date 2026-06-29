# Phase 2 — Python / PyTorch runtime

## Why
Our default engines are C++ ports — `llama.cpp` (text) and `stable-diffusion.cpp`
(image/video) — fed GGUF weights. They're tiny, quantized, and CPU-offloadable,
which is why a 12 GB GPU punches above its weight. But **most new models ship as
PyTorch first**, and many never get a C++/GGUF port: HunyuanVideo, **FramePack**
(seamless long video on low VRAM), Open-Sora, SVD, AnimateDiff, modern TTS
(Kokoro, XTTS, Parler), Flux variants, etc. A second "python" runtime lets us run
those, opting in per-model while keeping `cpp` as the default.

## How it fits the existing architecture
The factory is a Docker-image builder + HTTP-shim. A new runtime is just another
Dockerfile + gate, routed by a catalog field — no structural change.

```
catalog.Model.runtime: "cpp" (default) | "python"
        │
        ├─ cpp     → docker/Dockerfile.{cpu,cuda,vulkan}  + llmgate/videogate
        └─ python  → docker/Dockerfile.python.{cuda,cpu}  + pygate (new shim)
                         │
                         └─ torch + (ComfyUI | diffusers | TGI) serving an HTTP API
```

Already wired (Phase-2 foundation, this commit):
- `catalog.Model.Runtime` + `Rt()` (default `cpp`).
- **Resource footprint** is runtime-aware: a python/fp16 model needs ~2× its
  GGUF-equivalent VRAM and **can't CPU-offload**, so the run guardrail and the
  budget bars rate it correctly (it will, correctly, refuse to start a 24 GB
  fp16 model on a 12 GB card).
- **`hwPerf`** rates python models honestly: CPU = impractical (no GGUF), GPU =
  hard VRAM floor (no offload to lean on).

Still to build (Part B — the heavy bit): `docker/Dockerfile.python.cuda`, the
`pygate` shim, build/run routing for `runtime == "python"`, and the first model.

## The decision: which runtime to wrap first
The Dockerfile + gate is an ~6–10 GB image (torch + CUDA), so the first target
should be **tractable, genuinely useful, and fit 12 GB**. Options:

| Option | What | Pros | Cons |
|---|---|---|---|
| **A. ComfyUI server** | Wrap ComfyUI's HTTP API; models = workflow JSON + checkpoints | One runtime → tons of image/video models incl. **FramePack** (the long-video goal); huge ecosystem; good low-VRAM memory mgmt | Workflow templating per model is fiddly; heaviest |
| **B. FastAPI per-model shim** | A small `pygate` that loads one model (diffusers/transformers) and serves `/generate` | Simple, predictable, mirrors videogate; easy to reason about | One wrapper per model family; less leverage |
| **C. vLLM / TGI** | OpenAI-compatible text server | Drop-in for `/v1/chat`; great throughput | Text only (we already have llama.cpp); needs lots of VRAM (fp16) |

**First-model candidates that fit 12 GB and show real value:**
- **Kokoro TTS** (~82M, Python-only) — markedly better than our Piper TTS; tiny,
  even runs on CPU. Great low-risk "prove the python runtime" pick (Option B).
- **FramePack** — the strategic long-video unlock; designed for 6 GB+ VRAM
  (Option A, ComfyUI, or a dedicated wrapper). Higher effort, highest payoff.

## Recommendation
Prove the runtime with a **small Option-B model first (Kokoro TTS)** — it
de-risks the whole python path (Dockerfile, pygate, routing, build/run guardrail,
UI) on something tiny and clearly better than what we have — **then** invest in
**ComfyUI + FramePack** for seamless long video, which is the real prize.
