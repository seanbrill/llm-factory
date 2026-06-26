# local-llm factory — Documentation

Welcome! This is the full documentation for **local-llm factory** — a small app that
turns open-source AI models into ready-to-run, self-contained containers you can
build, run, and chat with on your own computer (no cloud, no data leaving your
machine).

These docs assume **no prior knowledge** and **nothing installed** on your machine.
Every technology we use has a plain-English "101" so you can follow along even if
you've never touched a container before.

## Table of contents

### Start here
1. [Getting started](getting-started.md) — install the one prerequisite and build
   your first model, step by step.
2. [Architecture overview](architecture.md) — what the pieces are, how a build and a
   run actually flow through the system, and where the code starts.

### Concepts — "101" guides (read what you're curious about)
3. [Containers 101: Docker & Podman](concepts/01-containers.md) — what a container
   is, why we use one, and the difference between Docker and Podman.
4. [GPU on a Mac 101: krunkit, Vulkan & Venus](concepts/02-gpu-on-mac.md) — how an
   Apple-Silicon Mac runs AI on its GPU *inside* a Linux container (the hard part we
   solved).
5. [Models 101: GGUF, llama.cpp & quantization](concepts/03-models-and-llamacpp.md) —
   what a "model file" is, what those `Q4_K_M` suffixes mean, and how it runs.
6. [Modalities & runtimes](concepts/04-modalities-and-runtimes.md) — chat, vision,
   embeddings, image generation, speech — what each is and which engine serves it.
7. [Go & the codebase](concepts/05-go-and-the-code.md) — a tour of the source so you
   can find and change things.

### Reference
8. [Running the factory itself in a container](CONTAINER.md) — the existing deep-dive
   on the Docker-out-of-Docker setup.

## The 30-second mental model

```
You pick a model in the web UI
        │
        ▼
The factory downloads it, bakes it into a container image,
and (for GPU) compiles the right inference engine
        │
        ▼
You click "Run" → the image starts as a container serving an
OpenAI-compatible API on a local port
        │
        ▼
You chat with it at http://<name>.localhost (a friendly local URL)
```

Everything runs on **your** machine. The only thing that ever touches the internet
is the one-time **download** of the model weights from Hugging Face.
</content>
