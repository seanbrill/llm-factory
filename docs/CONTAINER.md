# Built image — technical spec & integration contract

This document describes **what the `local-llm` factory produces** and **how other
applications should talk to a running container**. The factory is a build tool;
the *deliverable* is a portable Docker image (plus a `.tar` export) that serves
one GGUF model behind an OpenAI-compatible HTTP API.

---

## 1. What a build outputs

A single build produces three artifacts:

| Artifact | Location | Purpose |
|---|---|---|
| **Docker image** | local Docker daemon, tagged as you named it (e.g. `local-llm/qwen2.5-3b:latest`) | The runnable deliverable. Self-contained: model + server + shim baked in. |
| **Image tarball** | `./images/<name>_<tag>.tar` (`docker save`) | Portable copy to ship to another host / registry-less transfer. |
| **Model weights** | `./models/<file>.gguf` | The downloaded GGUF, cached and reused across builds. |

The image is **self-contained** — it does not download anything at runtime and
needs no volumes. Ship the tag (push to a registry) or the `.tar`.

### What the image contains — and what it does NOT

**Inside the image (all there is):**
- `llama-server` — the llama.cpp inference engine
- `llmgate` — the small Go entrypoint shim (supervisor + prompt injector)
- `/models/model.gguf` — the baked model weights
- `/etc/llmgate/system_prompt.txt` — the optional baked init prompt

**Not in the image (these live in *your* application, not here):**
- ❌ No queue or worker — **no BullMQ**, no Redis, no job processing
- ❌ No application/business logic, database, or auth
- ❌ No Python, no Node runtime, no Ollama

The container is a **stateless HTTP model server** and nothing more. Anything
that *drives* it — a BullMQ worker, your Go backend, your Next.js app — runs as a
separate process/service and talks to it over HTTP. The integration examples in
[§7](#7-integrating-from-your-apps) are **caller-side code that belongs in your
repo**, shown only to illustrate how to call the container. None of it is baked
into the image.

### Image labels
Every image carries metadata labels (queryable with `docker inspect` /
`docker images --filter`):

```
local-llm.tool=builder
local-llm.model=<catalog id, e.g. qwen2.5-3b>
local-llm.compute=<cpu|cuda>
```

---

## 2. Image anatomy

```
/usr/local/bin/llmgate              # PID 1: supervisor + prompt-injecting proxy (Go, static)
/usr/local/bin/llama-server         # inference engine (llama.cpp, statically linked)
/models/model.gguf                  # the baked model weights
/etc/llmgate/system_prompt.txt      # the baked initialization prompt (may be empty)
```

Two processes run inside the container:

```
client ──HTTP :8080──► llmgate ──HTTP 127.0.0.1:8081──► llama-server ──► GGUF
                         (injects baked system prompt into /v1/chat/completions)
```

- **`llama-server`** binds the *internal* port only (`127.0.0.1:8081`) and is never
  exposed directly.
- **`llmgate`** binds the *public* port (`:8080`), proxies everything verbatim
  (including SSE streaming), and augments `POST /v1/chat/completions` with the
  baked system prompt. If no prompt was baked, it is a transparent passthrough.
- `llmgate` is PID 1, so `docker stop` (SIGTERM) triggers a clean shutdown of
  `llama-server`.

### CPU vs CUDA variant

| | CPU image | CUDA image |
|---|---|---|
| Base (runtime) | `debian:bookworm-slim` | `nvidia/cuda:12.4.1-runtime-ubuntu22.04` |
| llama.cpp build | AVX2/FMA/F16C, portable | `GGML_CUDA=ON`, archs 75/80/86/89 |
| Default `CTX_SIZE` | 4096 | 8192 |
| Default `NGL` (GPU layers) | 0 | 99 (all) |
| Run requirement | none | `--gpus all` + NVIDIA Container Toolkit |

---

## 3. Runtime configuration (environment variables)

All tunables have baked defaults; override with `docker run -e KEY=VALUE`.

| Var | Default (CPU / CUDA) | Meaning |
|---|---|---|
| `PORT` | `8080` | Public port served by `llmgate`. |
| `UPSTREAM_PORT` | `8081` | Internal `llama-server` port. |
| `MODEL_PATH` | `/models/model.gguf` | Path to the GGUF inside the image. |
| `CTX_SIZE` | `4096` / `8192` | Context window (tokens). |
| `THREADS` | `4` | CPU threads — set to host vCPU count. |
| `PARALLEL` | `1` | Concurrent request slots (continuous batching). |
| `NGL` | `0` / `99` | Layers offloaded to GPU (CUDA only). |
| `INJECT_MODE` | `missing` | `missing` = inject prompt only if client sent none; `always` = force it. |
| `SYSTEM_PROMPT_FILE` | `/etc/llmgate/system_prompt.txt` | Baked prompt location. |
| `SYSTEM_PROMPT` | _(unset)_ | Inline prompt; overrides the file if set at run time. |
| `EXTRA_ARGS` | _(unset)_ | Extra flags appended to `llama-server` (advanced). |

---

## 4. The baked initialization prompt

Configured in the GUI ("Initialization prompt"), stored at
`/etc/llmgate/system_prompt.txt`, and applied by `llmgate` to
`POST /v1/chat/completions` only:

- **`INJECT_MODE=missing` (default):** if the request's `messages` already
  contains a `system` message, the body is forwarded unchanged; otherwise the
  baked prompt is prepended as a `system` message. Lets callers override.
- **`INJECT_MODE=always`:** any client-supplied `system` messages are removed and
  the baked prompt is forced to the front. Use this to *guarantee* behavior.

Notes:
- Injection applies to the **chat** endpoint only. `/v1/completions` (raw text)
  is not modified.
- You can change behavior at run time without rebuilding:
  `docker run -e INJECT_MODE=always -e SYSTEM_PROMPT="You are X" ...`.
- Malformed JSON bodies are passed through untouched (so errors surface from
  `llama-server`, not the proxy).

---

## 5. Running the image

```bash
# CPU
docker run --rm -p 8080:8080 local-llm/qwen2.5-3b:latest

# GPU (CUDA image)
docker run --rm --gpus all -p 8080:8080 local-llm/qwen2.5-3b:latest

# From an exported tarball on another machine
docker load -i qwen2.5-3b_latest.tar
docker run --rm -p 8080:8080 local-llm/qwen2.5-3b:latest

# Override tunables
docker run --rm -p 9000:9000 -e PORT=9000 -e THREADS=8 -e CTX_SIZE=8192 \
  local-llm/qwen2.5-3b:latest
```

**Readiness:** the model loads at startup (a few seconds to tens of seconds).
Until then `/health` returns non-200 and the container HEALTHCHECK reports
`starting`. Wait for `/health` 200 before sending traffic.

### Resource guidance (Q4_K_M)
Rule of thumb: **RAM/VRAM ≈ model size + ~1 GB + KV cache** (KV grows with
`CTX_SIZE` × `PARALLEL`).

| Model size on disk | CPU RAM | GPU VRAM |
|---|---|---|
| 1–2 GB (1B–3B) | 3–4 GB | 2–4 GB |
| 4–5 GB (7B–8B) | 8–10 GB | 6–8 GB |
| ~9 GB (14B) | 16 GB | ≥12 GB |

---

## 6. HTTP API surface

The image exposes the standard `llama.cpp` server routes on `PORT`. The ones you
will use:

| Method & path | Purpose |
|---|---|
| `GET /health` | Readiness/liveness — 200 when the model is loaded. |
| `GET /v1/models` | OpenAI-style model list. |
| `POST /v1/chat/completions` | **Primary** chat endpoint (OpenAI-compatible; system prompt injected here). |
| `POST /v1/completions` | Raw text completion (no injection). |
| `GET /props` | Server properties (context size, etc.). |
| `GET /metrics` | Prometheus metrics. |

### Chat completions — request

```jsonc
POST /v1/chat/completions
Content-Type: application/json
{
  "model": "local",                 // ignored by llama.cpp (one model loaded)
  "messages": [
    {"role": "user", "content": "Summarize: AAPL beat earnings by 12%."}
  ],
  "temperature": 0.2,
  "max_tokens": 512,
  "stream": false,                  // set true for SSE token streaming
  "response_format": {"type": "json_object"}  // optional: force valid JSON
}
```

### Chat completions — response

```jsonc
{
  "choices": [
    {
      "index": 0,
      "message": {"role": "assistant", "content": "..."},
      "finish_reason": "stop"
    }
  ],
  "usage": {"prompt_tokens": 31, "completion_tokens": 88, "total_tokens": 119}
}
```

**Streaming:** with `"stream": true` the server emits `text/event-stream` chunks
(`data: {...}\n\n`, terminated by `data: [DONE]`). `llmgate` forwards these
unbuffered.

**Concurrency:** requests are served by `PARALLEL` slots with continuous
batching. With `PARALLEL=1` requests queue and run sequentially; raise it (and
size memory accordingly — it splits the context) for concurrent callers.

---

## 7. Integrating from your apps

Treat the container as an OpenAI-compatible endpoint at
`http://<host>:8080/v1`. Point any OpenAI SDK at that base URL (API key is
ignored). Set a generous client timeout — **CPU inference can take seconds**.

### curl
```bash
curl -s http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","content":"Hello"}],"max_tokens":64}'
```

### Go (stdlib)
Use the client in [`internal/llm`](../internal/llm/client.go):
```go
client := llm.NewClient("http://localhost:8080", "local", 120*time.Second)
text, err := client.Chat(ctx, []llm.Message{
    {Role: "user", Content: "Summarize: AAPL beat earnings by 12%."},
}, llm.ChatOptions{Temperature: 0.2, MaxTokens: 256})
```
For structured output, `AnalyzeSignal` asks for `response_format: json_object`
and decodes the JSON into a typed struct — copy that pattern per task.

### Node / TypeScript (frontend or worker)
```ts
const res = await fetch("http://localhost:8080/v1/chat/completions", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    messages: [{ role: "user", content: prompt }],
    max_tokens: 256,
  }),
  signal: AbortSignal.timeout(120_000), // CPU inference is slow
});
const data = await res.json();
const answer = data.choices[0].message.content;
```

### Optional: driving it from a queue worker

> ⚠️ **This code runs in your application, not in the container.** The image has
> no queue, no Redis, and no BullMQ — it's just the HTTP server above. The
> snippet below is illustrative caller-side code for *your* backend repo.

If you front the model with a queue, a worker pulls jobs and calls the container
per job over HTTP. Give each job its own deadline so one slow inference can't
wedge the worker, and re-queue on error:

```go
// --- in your backend service (not baked into the image) ---
for job := range jobs {                       // jobs sourced from your queue
    jobCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
    result, err := client.AnalyzeSignal(jobCtx, job.Signal) // HTTP -> container
    cancel()
    if err != nil { job.Retry(err); continue }
    job.Complete(result)
}
```

Note: **BullMQ is a Node.js library.** A Node worker can call the container
directly; a Go worker would consume BullMQ's Redis keys with a Redis client (no
official Go BullMQ SDK). Either way it's external to the image. Match worker
concurrency to the container's `PARALLEL` (or run multiple containers behind a
load balancer).

---

## 8. Operations

- **Logs:** both processes log to stdout/stderr → `docker logs` / CloudWatch.
- **Shutdown:** `llmgate` (PID 1) forwards SIGTERM to `llama-server`; honor
  `docker stop` grace periods.
- **Health/readiness:** gate `/health` proxies upstream; treat 200 as ready.
- **Scaling:** the image is stateless — scale horizontally by running more
  containers (each loads its own copy of the model into memory).
- **Reproducibility:** builds pull `llama.cpp` at `master` by default. For
  repeatable images, pin a tag via the Dockerfile `ARG LLAMA_CPP_REF` (e.g. a
  release tag) before building.

---

## 9. Cloud deployment (optional)

[`deploy/ecs-task-definition.json`](../deploy/ecs-task-definition.json) runs a
built image on AWS Fargate (2 vCPU / 4 GB baseline; `MODEL_PATH=/models/model.gguf`
already matches these images). Push the image to ECR, fill in the placeholder
account/region/role ARNs, and `aws ecs register-task-definition`. Bump memory to
8 GB if you raise `CTX_SIZE`/`PARALLEL` or hit `OOMKilled` (exit 137).

---

## 10. Running the factory itself in Docker

The **factory** (the build GUI) runs as a container, so you can use it on any
machine with Docker and no Go toolchain. Build/run it with
[`start.ps1`](../start.ps1) / [`start.sh`](../start.sh) (stop with
[`stop.ps1`](../stop.ps1) / [`stop.sh`](../stop.sh)). What those scripts set up:

```
docker run -d --name local-llm-factory \
  -p 8799:8799 \
  -v /var/run/docker.sock:/var/run/docker.sock \   # Docker-out-of-Docker
  -v "$PWD/models:/app/models" \                   # weights persist on host
  -v "$PWD/images:/app/images" \                   # tarballs persist on host
  -v "$PWD/config:/app/config" \                   # editable catalog persists
  --add-host host.docker.internal:host-gateway \   # so "Test" can reach models
  local-llm-factory
```

Key points:
- **Docker-out-of-Docker:** the factory shells out to `docker`, which talks to the
  *host* daemon via the mounted socket. So **model images and containers it
  creates live on the host**, exactly as if you'd built them yourself — not
  nested inside the factory container.
- **Published ports are on the host.** When you click *Run* for a model on port
  `8080`, that port is published by the host daemon → reach it at
  `http://localhost:8080` from your machine.
- **The "Test" panel** runs inside the factory container, so it reaches model
  containers via `host.docker.internal` (set by `MODEL_HOST`, default
  `host.docker.internal` in the image). Natively it defaults to `127.0.0.1`.
- **Persistence:** because `models/`, `images/`, and `config/` are bind-mounted,
  your downloads, exported tarballs, and catalog edits survive restarts and are
  visible on the host.
- **Stopping the factory does not stop model containers** — they're independent
  on the host daemon. Remove them from the UI or with `docker rm -f <name>`.
- **Security note:** mounting the Docker socket grants the factory container
  root-equivalent control of the host daemon. Run it only on machines you trust.
