# Models 101: GGUF, llama.cpp & quantization

> **One-line version:** an AI model is a big file of numbers ("weights"). We use a
> compact file format called **GGUF** and a fast C++ program called **llama.cpp** to
> run it. "Quantization" shrinks the file so it fits on normal hardware.

## What *is* a model?

A language model is a huge mathematical function trained to predict the next word.
Its "knowledge" is stored as billions of numbers called **weights** (also called
**parameters**). When you see "7B," that means **7 billion** parameters.

To run a model you need:
1. The **weights** (the big file).
2. A **program** that loads the weights and does the math to generate text.

## GGUF — the file format

**GGUF** is a single-file format that packs the weights *and* the model's settings
(how to tokenize text, the chat template, etc.) into one `.gguf` file. One file =
easy to download, bake into an image, and run. That's why the whole catalog is GGUF.

## llama.cpp — the engine

**llama.cpp** is an open-source C++ program that runs GGUF models very efficiently on
CPU **and** GPU (via Vulkan/CUDA/Metal). It includes **`llama-server`**, which serves
a model over an **OpenAI-compatible HTTP API** — the same shape as the big cloud
APIs — so any tool that speaks "OpenAI" can talk to your local model.

The factory **compiles llama.cpp from source** inside the model image, with the GPU
backend turned on for GPU builds. That's the slow part of a first GPU build (~10
min); the compiled engine is then reused for later builds.

## Quantization — why the `Q4_K_M` suffix?

Full-precision weights are large (a 14B model is ~28 GB). **Quantization** stores the
numbers with fewer bits, shrinking the file a lot for a small quality cost:

| Suffix | Bits (approx) | Size of a 14B model | Quality |
|---|---|---|---|
| `F16` | 16 | ~28 GB | best, rarely needed |
| `Q8_0` | 8 | ~15 GB | excellent |
| **`Q4_K_M`** | ~4.5 | **~9 GB** | **great balance — our default** |
| `Q4_0` / `Q3` | 3–4 | smaller | lower quality |

`Q4_K_M` is the sweet spot most people use: big size savings, very little quality
loss. The catalog lists each model's size so you can match it to your RAM.

## "Context size" — the model's short-term memory

The **context** (or context window) is how much text the model can "see" at once —
your prompt plus its reply plus the conversation so far — measured in **tokens** (a
token is roughly ¾ of a word). A 16384 context ≈ ~12,000 words.

- Bigger context = remembers more, but uses more memory (RAM/VRAM).
- The factory lets you set `CTX_SIZE` at build time. Defaults: 4096 on CPU, 8192 on
  GPU. Raise it for long documents if you have the memory.

## Putting it together

```
qwen3-4b-Q4_K_M.gguf   ← the model (weights + settings), 4-bit quantized, ~2.5 GB
        │
        ▼ loaded by
llama-server (compiled with Vulkan)  ← does the math, on GPU if available
        │
        ▼ serves
http://localhost:PORT/v1/chat/completions  ← OpenAI-compatible API you chat with
```
</content>
