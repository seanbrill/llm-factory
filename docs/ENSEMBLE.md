# Ensembles

An **Ensemble** combines single-purpose model images (chat, vision, speech in
and out, image and video generation) into one unit with one OpenAI-compatible
endpoint and, optionally, a persona that speaks in its own voice.

No locally runnable model does chat, vision, speech and image or video
generation by itself: image and video come from diffusion models, and speech
from separate architectures. An Ensemble puts these specialists, its
**members**, behind a small front door called the **Conductor**
(`cmd/ensemblegate`). The Conductor starts each member when a request needs
it, keeps the members under a memory budget, and routes each request.

This page covers what is built, how to use it, and how it works. For a
step-by-step check of a whole ensemble, see [ENSEMBLE-TEST.md](ENSEMBLE-TEST.md).

---

## 1. Status

| Area | Status | Where |
|---|---|---|
| Compose from built images (one member per job) | ✅ built | Ensembles page, `internal/ensemble` |
| Build into an image, export `.tar` | ✅ built: stages the Conductor module offline | `internal/builder/ensemble.go`, `scripts/docker/runtimes/Dockerfile.ensemble` |
| Run and stop from the Ensembles page | ✅ built: stopping removes the members too | `/api/ensembles/run`, `/api/ensembles/stop` |
| Member lifecycle: start on demand, health check, states | ✅ built: stopped, starting, ready, failed, stopping | `cmd/ensemblegate/manager.go` |
| Memory budget, LRU eviction, keep warm | ✅ built | same |
| Recovery after a Conductor restart | ✅ built: adopts running members; a monitor marks dead ones failed | same |
| **Agent routing**: the chat member is the brain, the others are its tools | ✅ built, the default | `cmd/ensemblegate/agent.go` |
| Heuristic routing (keyword classifier) | ✅ built: the fallback | `cmd/ensemblegate/router.go` |
| Tool-calling conductor (a small model picks one member) | ✅ built: optional | same |
| Built-in agent tools for the brain (web search, fetch, files, skills) | ✅ built: off by default | ensemble `harness` field |
| Persona on an ensemble (prompt applied to chat) | ✅ built | `/ensemble/config` |
| Persona voice: built-in voice or a cloned voice | ✅ built | `internal/voice` |
| Voice clips: record or upload, consent, transcript by STT | ✅ built | `/api/voices`, Personas page |
| `/v1/chat/completions` with media in and out | ✅ built | Conductor |
| `/v1/audio/transcriptions` | ✅ built: whisper.cpp and audio.cpp (Moonshine) | Conductor |
| `/v1/audio/speech` in the persona's voice | ✅ built | Conductor |
| Voice chain: speech → chat → speech | ✅ built | `/ensemble/voice-chat`, Playground mic |
| Multi-step chains (draw, then animate, then say) | ✅ built through agent routing | the brain calls several tools in one turn |
| Running ensembles in the factory's `/v1` gateway | ✅ built: listed as `ens-<id>` | `internal/server/harness.go` |
| Playground: chat, mic, spoken replies, media inline | ✅ built | Playground |
| Streaming speech (audio.cpp `stream_format`) | ◑ the request shape passes through; nothing plays it as it streams yet | `voice.SpeechRequest.StreamFormat` |
| Real-time voice, live avatar | ⬜ not built (left room for: streaming speech, the Moonshine live endpoint) | — |
| Video understanding (frame sampling) | ⬜ not built: a video sent to an ensemble isn't watched | §9 |
| Embedded packaging (every runtime in one image) | ⬜ not built: `package_mode: "embedded"` runs orchestrated | — |
| Podman as well as Docker | ✅ built, run once on a Mac (Podman, libkrun, Vulkan) | §11 |
| Members with mounted weights (model-less runtime) | ✅ built, run once | §11, [RUNTIME.md](RUNTIME.md) |
| The caller's own tools handed back (`finish_reason: "tool_calls"`) | ✅ built | §11 |
| Members named by registry address | ◑ built, not yet run: the engine pulls a missing image | §11 |

Nothing above has been run against real models from this branch's tests:
every member type is faked with an HTTP test server, and the member lifecycle
runs against a fake engine CLI. [ENSEMBLE-TEST.md](ENSEMBLE-TEST.md) is the
first real run.

---

## 2. Using an ensemble

1. **Build the members.** On the Catalog page, build an image for each job you
   want: a chat model (required), and any of vision, speech-to-text, speech,
   image and video. Build them for **Docker**: the Conductor starts members with
   Docker.
2. **Make a persona** (optional) on the Personas page: a name, a prompt, and a
   voice. For a cloned voice, record or upload 3 to 30 seconds of speech, tick
   *"This is my voice, or I have permission to use it"*, and check the
   transcript.
3. **Compose the ensemble** on the Ensembles page: pick one image per slot, the
   persona, the routing (Agent unless you have a reason), and a memory budget.
4. **Build image.** It builds `local-llm/ens-<id>:latest`: the Conductor plus a
   manifest of the members. The members' images aren't copied in.
5. **Run.** The Conductor starts on a free port from 8200. Members start the
   first time a request needs them (or at once, if marked *Keep warm*).
6. **Open in Playground**, or call it through the gateway (§7).

Changing the persona or its voice doesn't need a rebuild: the factory pushes
them to the running Conductor. Changing members, routing, the budget or the
built-in tools does.

---

## 3. How it fits together

```
 Playground / any OpenAI client
        │
        ▼
 factory  (:8799)  /api/chat/stream, /api/tts, /api/ensembles/voice-chat, /v1 gateway
        │  pushes persona + voice (POST /ensemble/config), saves media to /api/media
        ▼
 Conductor  local-llm/ens-<id>  (:8200…, docker socket mounted)
        │  routes: agent (chat member = brain) | heuristic | tool-calling
        │  manager: start on demand, health check, budget, LRU eviction
        ▼
 members  ens-<id>-<tool>  (sibling containers on ephemeral 127.0.0.1 ports)
   chat · see_image · transcribe · speak · generate_image · generate_video
```

- The Conductor runs with the host Docker socket and
  `host.docker.internal` (the factory's Run adds both for ensemble images). It
  reaches members at `host.docker.internal:<port>`; set `MODEL_HOST` to change
  that.
- Members are named `ens-<ensemble id>-<tool>` and labelled
  `local-llm.ensemble-member=<id>` and `local-llm.ensemble-tool=<tool>`. Each
  publishes `127.0.0.1::8080` (an ephemeral port), unless the member pins one.

---

## 4. Routing

`routing` on the ensemble picks how a chat request reaches its members.

### Agent (default, `"agent"`)

The **chat member is the brain**: a llama.cpp model that answers and calls the
other members as tools, through `internal/agent` (the same agent loop as the
llmgate harness). If there's no chat member, a vision member is the brain.

| Tool | Member | Notes |
|---|---|---|
| `generate_image` | `generate_image` | the image reaches the client directly; the brain is told it's attached |
| `generate_video` | `generate_video` | same |
| `speak` | `speak` | a clip in the persona's voice; not offered in voice chat (the reply is spoken anyway) |
| `look_at_image` | `see_image` | offered only when the brain can't see images itself |
| `search_web`, `fetch_url`, file tools, skills | built-in | only when the ensemble's `harness.tools` turns them on |

- Generated media never pass through the brain's context. They go to the
  client out of band: `delta.media` (streaming) or `message.media`, plus a
  markdown image or link in the text for clients that read only text.
- The brain is leased per step, so the budget can evict it while an image
  member works.
- **Fallback.** If the brain's model refuses tools (its chat template has no
  tool support: a 4xx/5xx mentioning tools, jinja or the template), that turn
  is retried without tools; if the template can't do tools at all, the
  ensemble switches to the heuristic for good and says so in its status
  (`router_note`). A brain that isn't llama.cpp (`runtime` other than `cpp`)
  routes with the heuristic from the start.
- `"harness": false` in a chat request skips the agent and passes the request
  straight to the chat member.
- The shell tool is refused for ensembles: the Conductor controls Docker on
  this machine.

### Heuristic (`"heuristic"`)

A keyword classifier: an image part goes to vision; "draw…/an image of…" to
image generation; "make a video…" to video; "say…/read this aloud" to speech;
everything else to chat.

### Tool-calling (`"tool-calling"`)

A small extra model (`conductor`, run as the `_conductor` member) picks one
member per request; if it can't decide, the heuristic's choice stands.

The response header `X-Ensemble-Route` says which route answered (`agent`, or
the member tool).

---

## 5. Members and their lifecycle

| State | Meaning |
|---|---|
| `stopped` | not running; the next request that needs it starts it |
| `starting` | its container runs; waiting for its health check |
| `ready` | answering |
| `failed` | the last start failed: `error` says why (with the container's last log lines); the next request retries |
| `stopping` | being removed (evicted, or stopped on request) |

- **Health check.** `GET /health`: 200 is ready, 502/503 still loading. A 404
  (servers without `/health`, such as sd-server) falls back to `GET /`, where
  any answer below 500 counts. A start that isn't healthy within 8 minutes
  fails.
- **Budget.** Each member counts its `vram_gb` (filled from the catalog when
  you save: `min_vram_gb` on a GPU, `min_ram_gb` on CPU). Starting a member
  that doesn't fit stops the least recently used idle, unpinned members. A
  member busy with a request is never evicted, and one bigger than the whole
  budget fails with a message saying so.
- **Keep warm** (`pinned`): started when the Conductor starts, never evicted.
- **Concurrent requests** for a stopped member start it once; every request
  waits for that one start. Stopping a member during its start cancels it, and
  the waiting requests get that outcome instead of restarting it.
- **Restart.** A restarted Conductor adopts members still running (by label),
  waits for their health, and a monitor (every 15 s) marks members whose
  container died as `failed`.
- **Stop.** Stopping the ensemble (Ensembles page, or Stop on its container)
  removes its members too. `docker stop` on the Conductor does the same.

---

## 6. Personas and the voice contract

A persona is `{id, name, prompt, voice?}` in `config/personas.json`. On an
ensemble, its prompt comes first in every chat (merged with the client's own
system message) and its voice is used for all speech.

### Voices

```jsonc
{ "kind": "" }                               // the speech member's default voice
{ "kind": "builtin", "name": "alba" }        // one of the speech member's voices
{ "kind": "clone", "clip": "v-0123456789ab" } // a clip in the voice store
```

### Clips

- A **clip** is 3 to 30 seconds of one person speaking, plus its transcript.
- Stored in `config/voices/` as `<id>.wav` and `<id>.json`: files `0600`,
  folder `0700`, git-ignored. Stored as 16-bit mono WAV at 48 kHz or less.
- Uploaded or recorded on the Personas page (or in the ensemble editor). The
  consent box, "This is my voice, or I have permission to use it", must be
  ticked; the time it was ticked is kept (`consent_at`, unix ms).
- The transcript is filled in by speech-to-text (the ensemble's, or any running
  speech-to-text model) and stays editable.
- **Clips never leave the machine.** They aren't baked into images or
  exported. The factory sends a clip only to a Conductor running on this
  machine, which holds it in memory and sends it only to its own speech member.
- Deleting a clip resets the personas that used it to the default voice.

| Endpoint | Does |
|---|---|
| `GET /api/voices` | `{voices: [{id, name, transcript, seconds, sample_rate, created_at, consent_at}]}` |
| `POST /api/voices` | multipart `file`, `name`, `transcript`, `consent=true` |
| `POST /api/voices/update` | `{id, name?, transcript?}` |
| `POST /api/voices/delete` | `{id}` |
| `GET /api/voices/audio?id=` | the WAV (`Cache-Control: no-store`) |
| `POST /api/voices/transcribe` | `{id, ensemble?}` → `{text, source}`; 409 when no speech-to-text is running |

### Speech requests

`POST /v1/audio/speech` on an ensemble:

```jsonc
{
  "model": "ens-helper",          // any value on the Conductor itself
  "input": "Hello there.",
  "voice": "",                    // "", "default", "persona" or a stock OpenAI name ("alloy"…) = the persona's voice;
                                  // any other name = that built-in voice of the speech member
  "response_format": "wav",
  "speed": 1.0,
  "ref_audio": "<base64 WAV>",    // optional: clone this clip instead (overrides the persona)
  "ref_text": "what the clip says"
}
```

The answer is `audio/wav`, with `X-Ensemble-Voice: clone` or
`builtin:<name>`. The Conductor maps the request to the speech member's own
shape in one place, `internal/voice/adapters.go`:

| Runtime | Adapter | Clone request |
|---|---|---|
| `audiocpp` (Pocket TTS, Qwen3-TTS, VoxCPM2, Ming, XTTS v2) | `audioCpp` | `{"model":"tts","input",…,"voice_ref":{"type":"base64","data":…},"reference_text"?}`; never `voice` with `voice_ref`; a clip over 5 MiB is re-encoded to 24 kHz 16-bit; streaming: `"response_format":"pcm"` plus `"stream_format":"sse"` or `"audio"` |
| `python` (Kokoro) | `pygate` | can't clone |
| `cpp` (Piper) | `piper` | can't clone |

The clone rules come from the catalog (copied onto the member when the
ensemble is saved): `voice_clone`, `clone_needs_transcript` (Qwen3-TTS Base),
and *clone only* (no built-in voices: Qwen3-TTS Base, XTTS v2). A voice the
speech member can't speak is refused with a sentence that says what to change:

- "the speech member (…) can't clone voices: … Give the persona a built-in voice, or make a voice-cloning model … the ensemble's speech member"
- "the speech member (…) needs the voice clip's transcript to clone it: …"
- "the speech member (…) has no built-in voices: it only speaks a cloned voice. …"

The Ensembles page shows the same sentence (`voice_error`) before you try.

### How the persona reaches the Conductor

The persona and voice are never baked into the image. The factory pushes
`{hash, persona: {id, name, prompt}, voice: {kind, name | clip, ref_audio,
ref_text}}` to `POST /ensemble/config` on the running Conductor, which keeps
it in memory. It pushes when the Conductor starts, and before chat, speech,
voice chat and gateway requests whenever the hash the Conductor reports
differs from the saved persona's. A restarted Conductor gets it again with
the next request.

---

## 7. Endpoints

### The Conductor (port 8200…, or through the factory)

| Endpoint | Does |
|---|---|
| `POST /v1/chat/completions` | chat; images in (`image_url`), audio in (`input_audio`, transcribed first); media out |
| `POST /v1/audio/transcriptions` (also `/inference`) | speech to text: multipart `file`, `language?` |
| `POST /v1/audio/speech` | text to speech in the persona's voice (§6) |
| `POST /ensemble/voice-chat` | the chain: multipart `file`, `messages` (JSON history), `language?` → `{transcript, route, reply: {content, media}, audio, speech_error?, timings}` |
| `GET /ensemble/status` | the members and their states, routing, budget, persona |
| `POST /ensemble/members` | `{tool, action: "start" | "stop"}` |
| `POST /ensemble/config` | the persona and voice (from the factory) |
| `GET /v1/models`, `GET /v1/harness`, `GET /health` | |

While a member starts or media generate, a streaming chat sends SSE comments
such as `: ensemble: Starting the image member (sdxl-turbo)…`, which clients ignore and the
Playground shows.

### The factory

| Endpoint | Does |
|---|---|
| `GET/POST /api/ensembles`, `POST /api/ensembles/delete` | saved ensembles; a new one whose name is taken is refused (409) |
| `POST /api/ensemble/build` | `{id}`: queue the image build |
| `GET /api/ensembles/status` | per ensemble: `built`, `running`, the Conductor's status (`gate`), `gate_error`, `voice_error` |
| `POST /api/ensembles/run`, `/api/ensembles/stop` | `{id}` |
| `POST /api/ensembles/member` | `{id, tool, action}` |
| `POST /api/ensembles/voice-chat` | multipart `file`, `port`, `messages`; media and audio come back as `/api/media/…` URLs |

### The `/v1` gateway

A running ensemble is listed in the factory's gateway (`GET /v1/models`) as
`ens-<id>`, with `local_llm.modality: "ensemble"`; its members aren't listed.
Through the gateway key you can call `/v1/chat/completions`,
`/v1/audio/speech` and `/v1/audio/transcriptions` on it:

```sh
KEY=…   # the gateway key on the Harness page
curl -s localhost:8799/v1/chat/completions -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"ens-helper","messages":[{"role":"user","content":"Draw a lighthouse at night"}]}'

curl -s localhost:8799/v1/audio/speech -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"model":"ens-helper","input":"Hello"}' -o hello.wav

curl -s localhost:8799/v1/audio/transcriptions -H "Authorization: Bearer $KEY" \
  -F model=ens-helper -F file=@question.wav
```

`/v1/completions` and `/v1/embeddings` are refused for ensembles.

---

## 8. Building

`internal/builder/ensemble.go`:

- `EnsembleManifest` normalizes and validates the ensemble, fills the harness
  (workspace `/workspace`, skills `/etc/ensemblegate/skills`), keeps only
  pinned member ports, and adds the `_conductor` member for tool-calling.
- The build stages `cmd/ensemblegate` with `internal/ensemble`,
  `internal/voice` and `internal/agent` (all standard library only) as a small
  Go module, plus `manifest.json` and `harness/`, and builds
  `scripts/docker/runtimes/Dockerfile.ensemble` offline (`GOPROXY=off`).
- Labels: `local-llm.kind=ensemble`, `local-llm.ensemble=<id>`,
  `local-llm.modality=ensemble`, `local-llm.model=` (empty, so the container
  lists), compute and engine.

---

## 9. Not built yet

- **Video understanding.** Planned as a frame-sampling shim: decode with
  ffmpeg, keep 8 to 16 frames (uniform, or where the scene changes), and send
  them to the vision member as ordered images with timestamps. Today a video
  sent to an ensemble isn't watched.
- **Embedded packaging.** Every runtime and weight in one image (or one native
  package). The data model keeps `package_mode`, but the Conductor runs
  orchestrated. The base for it is `internal/native`'s multi-process
  supervisor: an embedded Conductor would implement the manager's `Engine`
  (run, remove, port, state, list, logs) over supervised processes instead of the
  docker CLI.
- **Real-time voice and a live avatar.** Room has been left for them: speech
  requests carry `stream_format` for audio.cpp's streaming mode, and
  Moonshine's `/v1/audio/transcriptions/live` is untouched. Neither is wired to
  the Playground.

---

## 10. Files

| File | What |
|---|---|
| `internal/ensemble/ensemble.go` | the Ensemble model and store (`config/ensembles.json`), validation, routing modes, `HarnessConfig` |
| `internal/ensemble/wire.go` | factory ↔ Conductor types: labels, states, `Config`, `Status`, `Media` |
| `internal/voice/` | the voice contract, clip store, WAV handling, speech adapters |
| `cmd/ensemblegate/` | the Conductor: `gate.go` (HTTP), `agent.go` (agent routing), `router.go` (heuristic, tool-calling), `manager.go` (lifecycle), `members.go` (calls to members), `persona.go` |
| `internal/builder/ensemble.go` | manifest, staging, build, member cleanup |
| `internal/server/ensembles.go`, `voices.go`, `personas.go` | the factory API |
| `ui/src/pages/Ensembles.svelte`, `ui/src/components/ensembles/` | the Ensembles page |
| `ui/src/components/personas/VoiceEditor.svelte` | voice picking and clip recording |

---

## 11. Ensembles for an app's own agent

An app that runs its own coding agent (FileFreak's Fred) can use an ensemble
as its model: a coding model as the brain, and a vision model it asks through
`look_at_image`, so people can paste screenshots.

### The caller's tools

Send `/v1/chat/completions` with the app's own `tools` (read, edit, shell,
MCP), and **without** `"harness": false`: that turns off the brain's loop, and
`look_at_image` with it. The Conductor runs only its member tools
(`look_at_image`, `generate_image`, `speak`, …). It hands every call to one of
the caller's tools back, as `finish_reason: "tool_calls"`, for the app to run
and send back as a `tool` message. Build the ensemble without a `harness`, so
no built-in file or web tools are on (the shell is never on in an ensemble).
Images arrive as `image_url` content parts with a `data:` URI. The brain sees
"[image 1 attached: call look_at_image to see it]" and the vision member gets
the image. `TestCallerToolsComeBackWithImage` covers this.

### Members with mounted weights

Save the ensemble with `"weights": "mounted"` (and `"platform"`, such as
`"linux/amd64"`, for another architecture). When it's built, each llama.cpp
member is rewritten to run from the model-less runtime for its compute
(`local-llm/runtime:<compute>-<llama.cpp ref>-<arch>`, see
[RUNTIME.md](RUNTIME.md)), and the manifest names its weights:

```json
{"tool": "chat", "image": "local-llm/runtime:vulkan-v0.9.0-arm64", "compute": "vulkan",
 "weights": {"file": "Qwen3.5-9B-Q4_K_M.gguf", "sha256": "…", "size": 5680522464,
             "repo": "unsloth/Qwen3.5-9B-GGUF", "revision": "<40-hex commit>",
             "url": "https://huggingface.co/unsloth/Qwen3.5-9B-GGUF/resolve/<commit>/Qwen3.5-9B-Q4_K_M.gguf"},
 "mmproj": null, "ctx_size": 0}
```

`repo` and `revision` pin the file: the commit whose file has this SHA-256,
and `url` resolves that commit. The factory asks Hugging Face which commit the
catalog's URL is at now. If the file there has changed since it was
downloaded, the manifest leaves the revision out, keeps the catalog's URL and
the build log says so. Download it again to pin it.

The weights are downloaded first if they aren't yet, to be hashed, and the
hashes are kept in `config/weights-sha256.json`. Speech, image and video members
keep their baked images. Build the runtimes too (`POST /api/runtimes/build`).

To run it, put every member's weights in one folder and start the Conductor
with `WEIGHTS_DIR` naming that folder as the engine sees it. That's
`"weights_dir"` on `POST /api/ensembles/run`, or `-e WEIGHTS_DIR=…` on a
`docker run` of your own. The Conductor starts each such member with the folder
mounted read only at `/models`, `MODEL_PATH=/models/<file>`, `MMPROJ_PATH` for
a projector and `CTX_SIZE` when the member sets one. A member whose weights
are mounted refuses to start without `WEIGHTS_DIR`, and says so.

### Podman

With `"engine": "podman"`, the factory runs the Conductor with Podman's API
socket from inside the machine (`podman info`, `Host.RemoteSocket.Path`)
mounted at `/var/run/docker.sock`. It also passes `--security-opt
label=disable`, so SELinux lets the Conductor use the socket, and sets
`MODEL_HOST=host.containers.internal`. The Conductor's docker CLI drives
Podman's Docker-compatible API. Vulkan members get `--device /dev/dri`, CUDA
members `--gpus all`. Under Podman, CUDA needs the NVIDIA Container Toolkit's
CDI setup, which Podman maps `--gpus` to.

To run the Conductor yourself under Podman:

```
podman run -d -p 127.0.0.1:8200:8080 \
  -v /run/podman/podman.sock:/var/run/docker.sock --security-opt label=disable \
  -e MODEL_HOST=host.containers.internal -e WEIGHTS_DIR=/Users/me/fred-weights \
  local-llm/ens-fred:latest
```

### Registry addresses

Set `"registry"` (such as `"ghcr.io/org"`) and the manifest names each member
`<registry>/<image name>:<tag>`, so an ensemble pulled on another computer
finds its members. The engine pulls a member image it doesn't have when the
member first starts, which makes that first start slower. The factory doesn't
push: tag and push the member and runtime images to that address yourself.

### The first real run (2026-11-18)

Run on a Mac (Podman 5, libkrun machine with 18 GB, Vulkan), from the factory's
API: the ensemble saved with `"weights": "mounted"`, `"engine": "podman"`,
built, and run with `weights_dir` set to the factory's `models/` folder.
Brain: Qwen3.5 9B. Eyes: MiniCPM-V 4.7 1B. (Qwen3 Coder 30B A3B, at about
18.6 GB of weights, doesn't fit that machine beside a vision member.)

- **Podman.** The Conductor got the machine's rootless socket
  (`/run/user/501/podman/podman.sock`), started both members from
  `local-llm/runtime:vulkan-v0.9.0-arm64` and reached them through
  `host.containers.internal`.
- **Mounted weights.** Each member had the folder mounted read only and
  `MODEL_PATH` set; each was ready in about 28 s.
- **A pasted screenshot** of a Go panic (`index out of range [3] with length
  3`, `main.parsePort`, `main.go:42`) went to the vision member through
  `look_at_image`. It read the error correctly, and the brain named the
  function and the line.
- **The caller's tools.** `read_file` came back as `finish_reason:
  "tool_calls"`. Given the file, the brain explained the bug and called the
  caller's `edit_file` with a correct fix (14 s).
- **Slow eyes.** The first `look_at_image` took 1 min 43 s, the vision member's
  start included. The projector runs on the CPU under Vulkan
  (`MMPROJ_NO_OFFLOAD=1`), so a large image is slow to encode. A smaller
  screenshot, or a GPU-offloaded projector where the driver allows it, would
  help.
