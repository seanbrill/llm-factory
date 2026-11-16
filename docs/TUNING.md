# Runtime tuning

How fast a llama.cpp chat model answers, and how much it can hold, depends on a
handful of llama-server settings. The factory calls the full set a **tuning**.
A tuning is:

1. **baked** into an image when it's built (`ENV` lines plus the label
   `local-llm.tuning`),
2. **saved** per image in `config/tuning.json`, so the next run uses it without
   a rebuild, and
3. **overridden** for a single run (`POST /api/run` with `"tuning"`).

At run time these merge **baked < saved < request**. They are validated and
priced against the engine's memory, and whatever differs from the image is
passed as `-e` variables. The context size changes without a rebuild too.

Code: `internal/tuning` (type, presets, estimate, GGUF reader),
`internal/gate/llama.go` (env to flags), `internal/builder/tuning.go` (bake),
`internal/server/tuning.go` and `tuningbench.go` (API, benchmark).

## Settings

| Field | Values | Trade-off |
|---|---|---|
| `preset` | `speed` `balanced` `long_context` `quality` `custom` | A preset alone (`{"preset":"speed"}`) is sized for the engine. Any fields you set beside it are kept. |
| `ctx_size` | 1024–1048576 | Longer context needs more KV memory. Capped at what the model was trained for. |
| `flash_attn` | `"auto"` `"on"` `"off"` (string) | On is faster and smaller. A quantized cache turns it on. |
| `kv_cache` | `f16` `q8_0` `q4_0` | `q8_0` halves the cache with little quality loss; `q4_0` cuts it to about a quarter. |
| `parallel` | 1–32 | Request slots. Two slots let an agent and a chat run at once. |
| `kv_unified` | bool | One shared cache across slots (only when `parallel` > 1). |
| `batch`, `ubatch` | 32–8192 | Prompt batch sizes. A larger `ubatch` speeds up prompt processing but uses more compute memory. |
| `spec` | `"none"` `"ngram"` `"draft"` (string) | Look-ahead (speculative decoding). `ngram` (llama.cpp's `ngram-mod`) needs no second model and is a big win when the answer repeats the prompt, as in code edits. `draft` uses a small paired model (the catalog's `draft`), baked at `/models/draft.gguf`. |
| `draft_max`, `draft_min` | 0–64 (draft), 0–1024 (ngram) | Tokens drafted per step. |
| `reasoning` | `"auto"` `"on"` `"off"` (string) | Thinking, for models whose template has `enable_thinking`. |
| `reasoning_effort` | `low` `medium` `high` | For gpt-oss-style templates. |
| `reasoning_budget` | -1 (no limit), 0, N | Cap on thinking tokens. |
| `cache_ram_mb` | -1, 0, N MiB | Host-RAM prompt cache across requests. The llama.cpp default is 8192, which is too much in a 12 GiB VM. |
| `ctx_checkpoints` | 0–64 | Saved states per slot for sliding-window and recurrent models, so a conversation that branches doesn't re-read the whole prompt. |
| `threads` | 0 = llama.cpp's choice | CPU threads. |
| `temperature`, `top_p`, `top_k`, `min_p`, `repeat_penalty` | numbers | Server-side sampling defaults. A request's own values still win. |

Normalization rules, reported as `notes`:
- A quantized cache turns flash attention on.
- Heads that don't divide into 32-element blocks keep `f16`.
- `kv_unified` is dropped when there's one slot.
- `ubatch` can't exceed `batch`.
- Look-ahead is off for vision and embedding models.
- `draft` without a baked draft is an error that suggests `ngram`.

## Presets and the estimate

Each preset is sized for the target engine's memory: Podman/Docker VM memory
minus 1 GB, two-thirds of a Mac's memory for native Metal, or memory minus
2 GB otherwise. The preset tries contexts from largest to smallest and keeps the
largest that fits. It then gives about half the spare memory to the prompt
cache (at most 4096 MiB) and some to checkpoints.

The estimate reads the model's GGUF header. That's the local file, or a 16 MiB
range request on its download URL, cached in `config/gguf-info.json`. It
computes the KV cache per layer and counts sliding-window layers only up to
their window. It adds:
- weights, plus the vision projector and the draft model;
- recurrent state;
- compute buffers;
- the prompt cache and checkpoints;
- about 0.35 GB of overhead.

When the header is missing, the estimate is a rough guess marked `"approx": true`.

| Preset | Choices |
|---|---|
| Speed | FA on, `q8_0`, `ngram`, 1 slot, 16K context (then 8K, 4K), low effort where supported |
| Balanced | `q8_0`, `ngram`, 2 unified slots, 32K context (then down) |
| Long context | trained context halving down to 8K; `q8_0`, then `q4_0` if needed |
| Quality | `f16`, no look-ahead, thinking on, high effort, 32K context (then down) |

## Benchmark

`POST /api/tuning/benchmark` runs candidates one at a time:
1. Start a container `localllm-tune-<id>` on an ephemeral `127.0.0.1` port,
   labelled `local-llm.tool=tune`.
2. Wait for `/health`, then send a warm-up request.
3. Send the workload through the gate with `"harness": false` at temperature 0,
   generating 256 tokens. `coding` is a ~1.5K-token Go file to revise; `chat` is
   a short question with a long answer.
4. Read llama-server's `timings` and `<engine> stats --no-stream`.

A candidate the estimate says won't fit a Vulkan engine is skipped. The
container is removed after each candidate, on error, on cancel, on factory
shutdown (SIGINT/SIGTERM) and at the next startup.

## API

```
GET    /api/tuning?ref=&engine=
  -> {ref, baked, saved|null, effective, source:"baked"|"saved",
      estimate, fits, warning, notes, error?,
      presets:[{id,label,explain,tuning,estimate:{weights_gb,kv_gb,total_gb,...},fits,warning}],
      model:{id,name,trained_ctx,vision,draft,draft_baked,modality,arch},
      engine:{name,compute,mem_gb,usable_gb}}
GET    /api/tuning/presets?model=&compute=&engine=   (engine docker|podman|native)
  -> {presets, model, engine}
POST   /api/tuning/estimate  {ref,engine,tuning} | {model,compute,engine,tuning}
  -> {tuning (normalized), estimate, fits, warning, notes:[string]}   (400 {error, notes})
PUT    /api/tuning  {ref, engine, tuning}  -> {saved, effective, estimate, fits, warning, notes}
DELETE /api/tuning?ref=&engine=            -> {ok}

POST /api/build  {..., "tuning": Tuning}        (ctx_size still works; it fills a tuning with no context)
POST /api/run    {..., "tuning": Tuning, "tuning_base": "saved"|"baked"}
  -> {..., tuning (effective), estimate}        (409 code engine_memory when a Vulkan run won't fit)
POST /api/native/package {..., "tuning": Tuning}

POST /api/tuning/benchmark {ref, engine, compute, candidates:[Tuning] | presets:[id], workload:"coding"|"chat", force}
  -> {id, total}
GET  /api/tuning/benchmark/{id}
  -> {id, ref, engine, workload, state:"running"|"done"|"error"|"canceled", step, total,
      current, phase, log, error, results:[{label, tuning, estimate, ok, error, skipped,
      load_s, prompt_tps, gen_tps, ttft_ms, mem_gb, draft_accept_rate|null, prompt_tokens, gen_tokens}]}
POST /api/tuning/benchmark/{id}/cancel
POST /api/tuning/benchmark/{id}/apply {index}  -> {saved, ref, engine}
```

## Environment variables

llmgate reads these and passes the matching llama-server flags. An empty value
means unset. A bad value stops llmgate with an error that names the variable.

| Variable | llama-server flag |
|---|---|
| `CTX_SIZE`, `PARALLEL`, `THREADS` | `--ctx-size`, `--parallel`, `--threads` |
| `FLASH_ATTN` | `--flash-attn` |
| `KV_CACHE_TYPE` | `--cache-type-k` and `--cache-type-v` |
| `KV_UNIFIED=1` | `--kv-unified` (only with `PARALLEL` > 1) |
| `BATCH_SIZE`, `UBATCH_SIZE` | `--batch-size`, `--ubatch-size` |
| `SPEC_TYPE=ngram` | `--spec-type ngram-mod`, plus `--spec-ngram-mod-n-max/-n-min` from `DRAFT_MAX`/`DRAFT_MIN` |
| `SPEC_TYPE=draft`, `DRAFT_MODEL_PATH` | `--spec-type draft-simple --spec-draft-model P`, `--spec-draft-n-max/-n-min` (skipped when the file is missing or empty) |
| `REASONING`, `REASONING_EFFORT`, `REASONING_BUDGET` | `--reasoning`, `--reasoning-effort`, `--reasoning-budget` |
| `CACHE_RAM_MB`, `CTX_CHECKPOINTS` | `--cache-ram`, `--ctx-checkpoints` |
| `TEMPERATURE`, `TOP_P`, `TOP_K`, `MIN_P`, `REPEAT_PENALTY` | `--temp`, `--top-p`, `--top-k`, `--min-p`, `--repeat-penalty` |

Native packages carry the same tuning in their manifest (`engine.tuning`), and
llmpack applies it. The command line's `--ctx-size` and `--threads` win.

## Checked in llama.cpp v0.9.0

- `--draft-max` and `--draft-min` were removed, and using them is an error. Use
  `--spec-draft-n-max/-n-min` and `--spec-ngram-mod-n-max/-n-min`.
- `--spec-type` defaults to `none`.
- A quantized V cache needs flash attention.
- Speculative decoding with a vision projector isn't refused, but the draft
  can't see the image ("drafts after multimodal input will be poor"), so the
  factory turns it off.
- A draft model with an incompatible vocabulary is logged, and the server runs
  without it.
- Defaults: `cache_ram` is 8192 MiB, 32 checkpoints per slot, batch
  2048/512, and flash attention `auto`.
