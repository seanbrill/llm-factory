# The model-less runtime

A normal factory build bakes the model into the image. A **model-less runtime**
is the same llama.cpp chat image without it: llama-server, llmgate, the
environment and the entrypoint. The weights are mounted when it runs. This is
what an app such as FileFreak's CLI ships, because a registry that refuses
layers over 10 GB can't carry most models inside an image. It downloads the
weights once, checks them, and mounts them into whichever runtime fits the
machine.

## The stages

Each llama.cpp Dockerfile (`scripts/docker/runtimes/Dockerfile.cpu`,
`.cuda`, `.vulkan`) has two final stages:

| Stage | Holds |
|---|---|
| `runtime-base` | llama-server, llmgate, the environment, the entrypoint. No model. |
| `runtime` (last) | `FROM runtime-base`, plus the model, mmproj, system prompt and harness. |

A plain build (no `--target`) gives the same image as before. Build with
`--target runtime-base` for the model-less one.

## Building one

From the factory's API (it queues like any build; watch it on the Builds page):

```
POST /api/runtimes/build
{"compute": "vulkan", "engine": "podman", "platform": "", "image_name": "", "tag": "", "export": false}
```

- `compute`: `cpu`, `cuda` or `vulkan`.
- `engine`: `docker` (default) or `podman`.
- `platform`: `""` builds for the engine's own architecture. `linux/amd64` or
  `linux/arm64` builds for that one (see below).
- `image_name` and `tag` default to `local-llm/runtime` and
  `<compute>-<llama.cpp ref>-<arch>`, for example
  `local-llm/runtime:vulkan-v0.9.0-arm64`.
- `export: true` also saves a `.tar` into `images/`.

The image is labelled `local-llm.tool=runtime`, `local-llm.compute`,
`local-llm.llama_cpp_ref`, `local-llm.arch` and `local-llm.engine`.

## Running one

```
podman run --rm --device /dev/dri -p 127.0.0.1:8080:8080 \
  -v <models dir>:/models:ro \
  -e MODEL_PATH=/models/Qwen3.5-9B-Q4_K_M.gguf -e CTX_SIZE=16384 \
  local-llm/runtime:vulkan-v0.9.0-arm64
```

For CUDA, use `--gpus all` in place of `--device /dev/dri`; on the CPU image,
use neither.

- **The model.** `MODEL_PATH` names the mounted `.gguf`. If there's nothing
  there, llmgate stops at once and says how to mount it.
- **A vision projector.** Mount it and set `MMPROJ_PATH` to it. Without one
  the model is text only.
- **A system prompt and a harness.** They are optional. Neither is baked in,
  so the model is served as it is. Mount a file and set `SYSTEM_PROMPT_FILE`
  (or `SYSTEM_PROMPT` inline) for a prompt, and `HARNESS_FILE` for a harness
  (docs/HARNESS.md). An app that runs its own agent sends `"harness": false`,
  and tool calls come back to it as `finish_reason: "tool_calls"`.
- **Every other setting.** `CTX_SIZE`, `NGL`, `PARALLEL`, `THREADS` and the
  tuning variables (docs/TUNING.md) work as they do on a baked image.

## Building for another architecture (amd64)

An engine builds for its own architecture: arm64 on an Apple-silicon Mac,
amd64 on most Windows and Linux PCs. NVIDIA machines, and so the CUDA images,
are amd64. There are two ways to get an amd64 image:

1. **Build on an amd64 machine.** This is the fast way. Run the factory there
   and build with no platform, or with `"platform": "linux/amd64"`.
2. **Build under emulation on an arm64 Mac.** Pass `"platform": "linux/amd64"`
   (for a model build too: `/api/build` takes the same `platform` field). The
   engine runs the amd64 compile through QEMU, which is many times slower than
   a native compile, since every step of compiling llama.cpp is emulated. Plan
   on an hour or more for CUDA.
   - **Docker Desktop** emulates amd64 out of the box.
   - **Podman** needs QEMU's user-mode emulation in its machine. Recent Podman
     machines include it. If a build fails with `exec format error`, run
     `podman machine ssh sudo rpm-ostree install qemu-user-static`, then
     `podman machine stop` and `podman machine start`.

Images for both architectures can share a name: push each with its own tag,
then join them with `docker manifest create` (or `podman manifest create`)
under one tag.
