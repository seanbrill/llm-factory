# Modalities & runtimes

> **One-line version:** a *modality* is the kind of input/output a model handles
> (chat, vision, audio, images…). The factory picks the right *runtime* (inference
> engine) for each modality automatically. Anything that runs on the GPU here is part
> of the **"ggml" family** — and only that family can use the GPU in a container.

## The modalities in the catalog

Each model has a `modality`, shown in the UI as a badge with an icon:

| Badge | Modality | What it does | Example models |
|---|---|---|---|
| 💬 Chat | `text` | General conversation, writing, Q&A | Qwen3, Gemma 3, Llama |
| 💻 Code | `code` | Programming help, completion | Qwen2.5-Coder |
| 🧠 Reasoning | `reasoning` | Step-by-step "thinking" answers | DeepSeek-R1 distills, QwQ |
| 👁 Vision | `vision` | Understands **images you send it** | Qwen2.5-VL, Gemma 3 vision |
| 🔢 Embeddings | `embedding` | Turns text into vectors (for search/RAG) | nomic-embed, bge |
| 🎨 Image gen | `image` | **Generates** images from a prompt | SDXL-Turbo |
| 🎙 Speech→Text | `audio-stt` | Transcribes audio to text | Whisper |
| 🔊 Text→Speech | `tts` | Speaks text aloud | Piper |

Use the **capability filter chips** at the top of the build form to show only one
kind at a time.

## The runtimes (which engine serves which modality)

Different jobs need different engines. The factory maps modality → runtime in
`runtimeFamily()` and uses a matching Dockerfile:

| Runtime | Serves | Endpoint | Dockerfile |
|---|---|---|---|
| **llama.cpp** (`llama-server`) | chat, code, reasoning, vision, embeddings | OpenAI `/v1/chat/completions`, `/v1/embeddings` | `Dockerfile.<compute>` |
| **stable-diffusion.cpp** (`sd-server`) | image generation | `/v1/images/generations`, `/sdapi/v1/txt2img` | `Dockerfile.sd.vulkan` |
| **whisper.cpp** (`whisper-server`) | speech-to-text | `POST /inference` (upload audio) | `Dockerfile.whisper.{vulkan,cpu}` |
| **Piper** (small Python wrapper) | text-to-speech | `POST /v1/audio/speech` | `Dockerfile.tts.cpu` |

### Why "ggml family"?
llama.cpp, stable-diffusion.cpp, and whisper.cpp are all built on the same math
library (**ggml**), which is what has the Vulkan GPU backend that works in a
container on a Mac. Tools *outside* this family (most Python/PyTorch AI) can't use
the Mac GPU in a container — that's a hard limit, not a missing feature.

## How vision works (the extra file)

A vision model needs **two** files: the language model **and** a *projector*
(`mmproj`) that turns an image into tokens the model understands. The factory
downloads both, bakes both in, and launches `llama-server --mmproj ...`. On the Mac
GPU it keeps the image-encoding step on the CPU (`--no-mmproj-offload`) to avoid a
known Venus bug; the rest runs on GPU.

## What about video generation?

Honest answer: **not practical here yet.** There's an experimental C++/Vulkan path
(Wan models via stable-diffusion.cpp), but its quality on the Mac GPU is poor and
it's slow. It's a *watch-item*, not a feature. For "understanding" a video, extract
a few frames and send them to a 👁 vision model instead.

## Speed expectations (rough, on an M4 Pro GPU)

- 💬 Chat 14B: ~20 tokens/sec · 8B: faster
- 👁 Vision: similar to chat once the image is encoded
- 🎨 Image (SDXL-Turbo, 512px, ~6 steps): ~6 seconds per image
- 🎙 Whisper / 🔊 Piper: faster than real-time on CPU
</content>
