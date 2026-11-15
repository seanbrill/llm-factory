# LLM Factory UI — design system and structure

The UI follows the knitted.studio design sheet **"LLM Factory — UI redesign"**
(`ds_01M7EKYBPS2BQ57JNQHB`, project `pj_01M7EKTSKM2Z8VHDZYKK`). Read a frame's
HTML/CSS with the knitted MCP `get_design` (`frameId`), look at it with
`view_design`.

| Page | Route | Frame |
|---|---|---|
| Brief, foundations | — | f18, f19 |
| Overview | `#overview` | f1 (desktop), f16 (phone) |
| Catalog (pick several) | `#catalog` | f8 |
| Build plan (preflight) | `#plan` | f9 |
| Builds (lanes, failures) | `#builds`, `#builds/<job id>` | f10 (desktop), f17 (phone) |
| Library | `#library`, `#library/<image ref or model id>` | f11 |
| Harness | `#harness` | f12 |
| System | `#system`, `#system/gpu`, `#system/memory`, `#system/archive` | f13 |
| Playground | `#playground`, `#playground/<port>` | f14 |
| ⌘K palette | anywhere | f15 |

Old page ids still work: `#build`→catalog, `#images|#containers|#models`→library,
`#chat`→playground, `#settings`→system.

## Tokens (src/app.css)

Use the semantic tokens, never raw hex (except inside the mark).

| Token | Use |
|---|---|
| `--cv` | page canvas |
| `--ink` | text |
| `--p1` `--p2` `--p3` | surfaces: cards, raised controls, chips/hover |
| `--ln` `--ln2` | lines: card borders, control borders |
| `--mu` `--fa` | muted text, faint text (labels, hints) |
| `--pr` `--onpr` `--prs` | primary (actions, progress, focus), text on it, soft primary fill |
| `--ac` | accent (secondary data series) |
| `--ok` `--oks` | ready, running, passed (+ soft fill) |
| `--wa` `--was` | needs you: too small, tight fit |
| `--er` `--ers` | failed, stopped, errors |
| `--gpu` `--gpus` | GPU (Vulkan, CUDA) |
| `--r` `--rs` `--rl` | radius: controls, small pieces, cards |
| `--font-display` `--font-body` `--font-mono` | Bricolage Grotesque, Geist, Geist Mono |

Status and GPU colours are fixed: green always means ready, amber "needs you",
red failed, cyan GPU.

## Global classes

- Buttons: plain `<button>` is styled; `.btn` makes a link look like one.
  Modifiers: `.pri` / `button.primary`, `.gh` / `button.ghost`, `.sm` /
  `button.small`, `.icon` (square), `.danger`.
- `.card` (surface, 14px radius, 18px padding). Card header: `<div class="hd"><h2>Title</h2><span class="chip">…</span><div class="sp">actions</div></div>`.
- Text: `.sub` (muted 13px), `.hint`, `.lbl` (11px uppercase label), `.mono`, `.empty`.
- `.chip` + `.ok` `.wa` `.er` `.gpu` `.pr` — status / compute / route tags.
- `.dot` (+ `.wa` `.er` `.off`) — live status dot.
- `.kbd` — a key hint.
- `.eng` (+ `.warn` `.down`) — engine pill.
- `.bar` (+ `.ok` `.wa` `.er` `.gpu`, `.busy` for indeterminate) with an `<i style="width:64%">` child.
- `.spin` — small spinner.
- `.callout` (+ `.er` `.wa` `.ok` `.gpu`): `<div class="callout wa"><Icon name="warn"/><span><b>What happened.</b><p>Why / fix.</p></span></div>`.
- `.code` / `<CodeLine cmd="…"/>` — a host command with a copy button.
- `<input type="checkbox" class="tg">` — toggle switch.
- `table.tbl`, `.stack` (vertical gap 18px), `.cols` (grid gap 14px).
- `.sr` — text for screen readers only.

Pages don't render their own `<h1>` — the top bar shows the page title.
Section titles are `<h2>` inside cards. Use scoped `<style>` for layout.

## Layout

Pages render inside `main.page > .inner` (24px 28px padding, max 1480px).
Breakpoints: **≤1100px** the rail shows icons only; **≤760px** phone layout —
rail hidden, brand header + engine pills on top, tab bar at the bottom (the
page gets bottom padding for it). Design for 1440 first, then check 1100 and a
390px phone.

## Icons

`<Icon name="…" size={16} />` (components/Icon.svelte, 1.8 stroke, 24 grid).
Names from the design: overview catalog builds library harness playground
ensembles personas system more warn check xmark errc info clock mac chip disk
terminal folder key sparkle bell bolt find copy external stopsq chain route
arrowRight chevronRight chevronUp chevronDown minus grip retry plug — plus the
older set (cpu gpu chat code eye image film mic speaker box trash download doc
refresh plus x alert globe …). Add new ones to src/lib/icons.ts.

## Data and state (src/lib)

- `router.ts` — `route` (page id store), `routeArg` (text after `#page/`),
  `go(page, arg?)`, `href(page, arg?)`, `PAGES`, `pageDef(id)`.
- `stores.ts` — `sysinfo` (per-engine memory/GPU in `.engines`), `catalog`,
  `personas`, `ensembles`, `resources`, `appVersion`, `toast(msg, ms?)`.
- `live.svelte.ts` — shared polled data: `live.containers`, `live.images`,
  `live.files` (catalog + downloaded/on_disk_gb), `live.storage`, `live.harness`,
  plus `resources`. In a component: `$effect(() => watch("containers", "storage"))`;
  after a change: `refresh("containers")`.
- `build.svelte.ts` — the build queue: `build.jobs` (BuildJob[] from
  /api/builds), `queue.loaded`, `views[jobId]` (streamed log, phase, pct),
  `watchQueue()`, `openLog(id)`, `stopJob(job)`, `retryJob(job)`, `moveJob(job, to)`.
- `builds.ts` — Builds page helpers: lanes with engine facts, step tracking,
  log views, fix ordering.
- `jobs.ts` — `lanes(jobs)`, `history(jobs)`, `counts(jobs)`, `progress(job)`,
  `phase(job)`, `timeText(job)`, `routeText(buildEngine, engine)`,
  `compiledOn(job)` (the server's `build_engine`).
- `explain.ts` — `explainFailure(job, log)` → `{tone, title, why, fixes[]}`;
  fixes carry a `cmd` to copy or an `action` (`retry` `cleanup` `system` `library`).
  `focusLine(lines)` finds the line a log should open on. `PODMAN_MEMORY_CMD`.
- `plan.svelte.ts` — the build plan (multi-select): `plan.items`, `addToPlan`,
  `removeFromPlan`, `togglePlan`, `inPlan`, `updatePlanItem`, `setPlan`,
  `defaultsFor(model, sysinfo)`, `predictRoute(...)`, `queueItems(items, catalog)`,
  `quickBuild(model, sysinfo)`.
- `harness.ts` — harness API types and calls.
- `baked.ts` — tools baked into an image (docs/HARNESS.md, "Baked harness"):
  `TOOLSETS`, `canHarness(model)`, the plan item's `harness` setting
  (`defaultHarness`, `harnessProblem`, `harnessBody` → the build's `harness`,
  `harnessFromConfig` for clones) and `imageHarness(image)` for the Library.
  `#harness/baked` opens its card on the Harness page.
- `filefreak.ts` — FileFreak archive (System page) types and calls; see docs/FILEFREAK.md.
- `format.ts` — `gb()`, `dur()`, `clock()`, `ago()`, `plural()`, `engineName()`,
  `computeName()`, `computeChip()`, `upFor()`.
- `ui.svelte.ts` — `openPalette(seed?)`, `copyText(text)`.
- `auth.svelte.ts` — access-token lock (any 401 `auth_required` shows the lock screen).

## API (Go, internal/server)

Reads: `GET /api/sysinfo`, `/api/catalog`, `/api/models`, `/api/images`,
`/api/containers`, `/api/resources`, `/api/personas`, `/api/ensembles`,
`/api/version`, `/api/builds`, `/api/builds/state?id=&offset=`, `/api/storage`,
`/api/harness/status`, `/api/container/logs?id=&engine=&tail=`.

Actions (POST JSON): `/api/build` (queues; `{status, id, position, engine}`),
`/api/build/preflight {items:[{model_id, engine, compute}]}`,
`/api/builds/cancel|retry {id}`, `/api/builds/move {id, to: up|down|top|bottom}`,
`/api/run`, `/api/stop {id, engine}`, `/api/image/delete {ref, engine}`,
`/api/image/download`, `/api/model/delete {model_id}`, `/api/storage/cleanup`,
`/api/system/reclaim`, `/api/gpu/probe`, `/api/chat/stream` (SSE),
`/api/image/generate`, `/api/video/generate`, `/api/transcribe`, `/api/tts`,
`/api/harness/*`, `/api/filefreak/*` (status, settings, key, key/remove,
folder/check, queue/retry, queue/remove). Shapes are in src/lib/types.ts, src/lib/harness.ts and src/lib/filefreak.ts.

## Writing

Plain, specific sentences. A problem always comes with its fix (a command to
copy or a button). Say what the machine really has; show "—" for unknown values
and never invent numbers. Name engines "Docker" and "Podman", computes "CPU",
"Vulkan", "CUDA". Routes read "Docker → Podman" (compiles on Docker, runs on
Podman).

## Seeing it without a factory

`npm run build`, then serve the build with the mock API (no containers, no
engines): `python3 -I <scratchpad>/mock/mockapi.py --port <p> --web ../internal/server/web`
and screenshot with `shot.sh URL out.png 1440 960` or `phone.sh BASE HASH out.png`.
Scenarios: `--scenario design` (default: Podman 2 GB, one build running, two
queued, last night's OOM failure), `healthy` (Podman 12 GB), `empty`.
