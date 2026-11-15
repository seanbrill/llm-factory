# Testing an ensemble end to end

This is the first real run of an ensemble: a persona that chats, writes code,
hears you, answers in a cloned voice, and makes images and video, all from the
factory. Each step says what you should see, and the table at the end says
where to look when you don't.

How ensembles work is in [ENSEMBLE.md](ENSEMBLE.md).

Before this, only automated tests had run (every member faked, see step 0).
Expect the first run to find something; the diagnosis table is there for it.

---

## 0. The automated tests (no models, no containers)

```sh
gofmt -l cmd internal
for os in darwin linux windows; do GOOS=$os go vet ./...; done
go test -race ./...
cd ui && npx svelte-check && npm run build -- --outDir /tmp/uibuild --emptyOutDir
```

The Conductor's tests (`cmd/ensemblegate`) fake every member type over HTTP
(llama.cpp chat with tool calls, whisper.cpp and audio.cpp speech-to-text,
audio.cpp speech, sd-server, video) and run the member lifecycle against a fake
`docker` CLI. They don't start a container or a model.

---

## 1. What to build

On the **Catalog** page, build each of these for **Docker** (the Conductor
starts members with Docker). Sizes are for a 12 GB NVIDIA GPU; the CPU column
is for a machine without one.

| Slot | GPU (CUDA) | CPU only | Why this one |
|---|---|---|---|
| Chat (the brain; also writes code) | `qwen3-8b` | `qwen3-4b` | llama.cpp with a tool-calling chat template: agent routing needs one |
| Speech to text | `whisper-small` | `whisper-small` | small and fast; `moonshine-streaming-small` (audio.cpp) also works |
| Speech (clones) | `pocket-tts-small-en` | `pocket-tts-small-en` | clones from a clip, transcript optional, has built-in voices too |
| Images | `sdxl-turbo` | `sd-1.5` | a few steps per image |
| Video | `wan2.1-t2v-1.3b` | (skip: far too slow on CPU) | the smallest video model |
| Vision (optional) | `qwen3.5-4b-vision` | `minicpm-v-4.7-1b` | reads images you attach; without it the brain is told it can't see |

To check the transcript path too, use `qwen3-tts-0.6b-base` as the speech
member: it needs the clip's transcript and has no built-in voices.

For coding, the chat member answers: pick a stronger coder as the chat member
if code matters more than chat. One chat member per ensemble.

**Expect:** each image in the Library, built for Docker.

---

## 2. Record the voice

1. **Personas → New persona.** Name `Nova`, prompt for example: *You are Nova,
   a cheerful assistant who keeps answers short.*
2. Under **Voice**, choose **Cloned**.
3. Tick **This is my voice, or I have permission to use it**. Recording stays
   off until it's ticked.
4. Record 8 to 15 seconds of ordinary speech in a quiet room (or upload a WAV
   of 3 to 30 s). Name the clip and **Save clip**.
5. The transcript fills in if a speech-to-text model is running; nothing is
   running yet, so type exactly what you said and **Save transcript**. (After
   step 5 you can press **Transcribe again** to let the ensemble do it.)
6. Save the persona.

**Expect:**
- The clip in the list with its length and "consent given <date>".
- On disk, owner-only files (nothing in git):
  ```sh
  ls -l config/voices/      # drwx------ folder; -rw------- v-<12 hex>.wav and .json
  git status --short config # nothing from config/voices
  ```
- Saving a second persona named `Nova` is refused: "a persona named "Nova"
  already exists".

---

## 3. Compose the ensemble

**Ensembles → New ensemble.**

1. Name it `Nova` (any name; the image is `local-llm/ens-nova`).
2. Pick one image per slot from step 1. Leave **Keep warm** off.
3. Persona: **Nova**. Her voice shows; with Pocket TTS no warning appears.
4. Routing: **Agent** (the default).
5. Built-in tools: leave them off for the first run.
6. Memory budget: 12 GB on the GPU machine (what your GPU has), or what Docker
   may use on CPU. The meter says whether everything fits at once; if not, the
   Conductor stops the least recently used member to start another. That's
   expected here: chat plus images plus video don't fit in 12 GB.
7. **Save ensemble.**

**Expect:** the card, with the routing chip **Agent**, and "Plays Nova and
speaks with a voice cloned from “<your clip's name>”". Saving a second ensemble with the same name is
refused (409) instead of replacing this one.

---

## 4. Build and run

1. **Build image** on the card. The log streams; it ends with the image
   `local-llm/ens-nova:latest`. The build compiles the Conductor offline, so it
   needs no network.
2. **Run.** A toast: "“Nova” is starting on :8200. Its members start when
   they're first needed." (The port is the first free one from 8200.)

**Expect on the card:**
- **Running on :8200**; every member **Stopped** (they start when needed).
- No red or yellow callouts. A yellow "Its voice won't work as set" means the
  speech member can't speak the persona's voice: it says why.
- The line under the members: "the chat member (qwen3-8b) is the brain; the
  other members are its tools".

From a terminal:

```sh
curl -s localhost:8200/ensemble/status | jq '{router, router_note, agent_tools, persona, members: [.members[] | {tool, state}]}'
```

`router` is `agent`, `agent_tools` lists `generate_image`, `generate_video`,
`speak` (and `look_at_image` if the brain can't see), and `persona.name` is
Nova once the factory has pushed it (within a second or two of Run).

---

## 5. In the Playground

**Open in Playground** on the card.

| Try | Expect |
|---|---|
| "Who are you?" | "Starting the chat member…" for up to a minute the first time, then an answer as Nova. The card shows chat **Starting…**, then **Ready**. |
| "Write a Python function that checks whether a string is a palindrome, with a test." | A code block. |
| "Draw a lighthouse at night." | "Starting the image member…" (on 12 GB the chat member may be stopped first to make room), then the image in the reply with a short caption. The image is saved under `media/`. |
| "Now make a short video of waves on a beach." | Minutes on a GPU. The video plays inline. |
| Turn on **Speak replies to typed messages** (speaker button by the composer), then ask anything | The reply plays in Nova's cloned voice. |
| Press the **mic**, ask "What's the capital of France?", press it again | Your words appear as your message, then the answer, spoken in Nova's voice. |
| Attach a photo and ask "What's in this picture?" | With a vision member: a description. Without one: Nova says she can't see images. |

On the Ensembles page meanwhile: members go **Starting… → Ready**, the memory
meter fills, and an evicted member goes back to **Stopped**.

---

## 6. Through the gateway

The gateway key is on the **Harness** page.

```sh
KEY=…
curl -s localhost:8799/v1/models -H "Authorization: Bearer $KEY" | jq '.data[] | {id, modality: .local_llm.modality}'
```

**Expect:** `ens-nova` with modality `ensemble`, and none of its members
(`ens-nova-chat` and so on aren't listed).

```sh
# Chat, as Nova
curl -s localhost:8799/v1/chat/completions -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"ens-nova","messages":[{"role":"user","content":"Say hi in five words."}]}' | jq -r '.choices[0].message.content'

# Speech in Nova's voice: the header says which voice
curl -s -D - localhost:8799/v1/audio/speech -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"ens-nova","input":"Hello from Nova."}' -o nova.wav | grep -i x-ensemble-voice
afplay nova.wav   # macOS; X-Ensemble-Voice: clone

# Speech to text
curl -s localhost:8799/v1/audio/transcriptions -H "Authorization: Bearer $KEY" -F model=ens-nova -F file=@nova.wav
```

**Expect:** `X-Ensemble-Voice: clone`, a WAV in the cloned voice, and the
transcription `{"text":"Hello from Nova."}` (or close).

---

## 7. Stop, and recovery

1. Crash the Conductor and start it again, with a member running:
   `docker kill localllm-8200 && docker start localllm-8200`. Within a few
   seconds the card shows the members that were running as **Ready** again
   (adopted, not restarted), and the persona is pushed again with the next
   request. (A graceful `docker stop` or `docker restart` removes the members
   first, so they come back **Stopped**.)
2. Kill one member: `docker rm -f ens-nova-chat`. Within 15 seconds its state
   is **Failed** ("it stopped on its own: its container is gone"); the next
   chat starts it again.
3. **Stop** on the card.

**Expect:** no containers left:

```sh
docker ps -a --filter label=local-llm.ensemble-member=nova   # empty
```

---

## Diagnosis

Start with the card on the Ensembles page, then the Conductor's log
(`docker logs localllm-8200`), then the member's own log while it runs
(`docker logs ens-nova-<tool>`). A member that fails to start is removed, and
its last log lines are in the error on the card.

| What you see | Where to look | Likely cause and fix |
|---|---|---|
| Build log fails at `go build` | the build log | the staged Conductor didn't compile: run `go test ./internal/builder -run Ensemble` and report the output |
| **Run** says "build the ensemble first" | | the image `local-llm/ens-<id>:latest` doesn't exist on Docker: build it |
| Card: "its Conductor didn't answer" | `docker logs localllm-8200` | still starting (seconds), or crashed: "read the manifest" means a bad build; "permission denied … docker.sock" means Docker refused the socket mount |
| A member **Failed**: "didn't answer its health check within 8m" | the error's log lines, `docker logs ens-nova-<tool>` | the model is still loading (big weights on CPU) or out of memory; raise Docker's memory or pick a smaller model |
| A member **Failed**: "needs ~N GB, more than the ensemble's whole memory budget" | | raise the budget, or pick a smaller model |
| A member **Failed**: "No such image" | | that member's image was deleted or built for Podman: rebuild it for Docker and save the ensemble again |
| The routing chip says **Agent (fell back)** | the note under the members (`router_note`) | the chat model's template can't call tools: pick a Qwen3 chat model, or use Heuristic routing |
| Asked for an image, got only text that says it drew one | `docker logs localllm-8200` (look for `generate_image`) | the brain didn't call the tool: rephrase ("draw…", "make an image of…"), or use a stronger chat model |
| "the speech member (…) can't clone voices" | the yellow callout on the card | the speech member isn't a cloning model: make `pocket-tts-small-en` (or another "clones voices" model) the speech member |
| "… needs the voice clip's transcript" | Personas → Nova → Voice | type what the clip says and **Save transcript** |
| "… has no built-in voices" | | the speech model speaks only a clone (Qwen3-TTS Base, XTTS): give the persona a cloned voice |
| Speech in a default voice instead of Nova's | `curl -s localhost:8200/ensemble/status \| jq .persona` | `voice` should be `clone`; if empty, the factory hasn't pushed the persona: reload the Ensembles page (it pushes on every status poll) |
| Mic: "no speech was heard" | | the recording was silent or too short: allow the microphone, speak closer |
| Mic: "speech to text failed" | the speech-to-text member's state | it failed to start, or the audio format: check `docker logs ens-nova-transcribe` |
| Mic works, but no audio comes back | the note under the reply (`speech_error`) | the text answer is kept; the reason is in the note |
| Video takes very long or fails with out of memory | `docker logs ens-nova-generate_video` | Wan needs the GPU; on 12 GB keep other members stopped (the budget does this) |
| Gateway: "model "ens-nova" is not running" | `GET /v1/models` | the Conductor isn't running, or the factory's container list is stale for a few seconds: retry |
| Gateway speech: "is a text model" | | the `model` named a plain model, not the ensemble |
