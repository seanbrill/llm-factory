# UI refactor plan — vanilla JS → Svelte + Vite + TypeScript

## Status: ✅ COMPLETE (migration done, vanilla UI removed)
All 8 pages ported to Svelte 5 + TypeScript; the three vanilla files
(`index.html`, `app.js`, `model-meta.js`, `style.css`) are deleted. `vite build`
emits the bundle to `internal/server/web/` (`index.html` + `assets/app.js` +
`assets/app.css`), which `go:embed` ships in the factory image. Verified:
- `svelte-check` → **0 errors**.
- `go build ./...` → OK (Go embeds the built output).
- `docker build -f Dockerfile.factory` → exit 0.
- **Runtime:** factory container serves `/` (Svelte index → `/assets/app.js`
  115 KB), `/assets/app.css`, and `/api/catalog` (37 models) — UI + API intact.
- All start scripts (Windows `.ps1` + Linux/macOS `.sh`, prod + dev) build the UI
  first via a throwaway `node:20-alpine` container — **no host Node required**.
  `stop` scripts kill the factory, which is the UI (one-script up/down).

The sections below are the original plan, kept for reference.

## Why
The UI grew from a small page into a real app: `internal/server/web/app.js` is
~1,900 lines, `index.html` ~330, `style.css` ~560 — all global. Symptoms:
- **Small alignment bugs** keep creeping in because everything shares one global
  stylesheet and hand-written `innerHTML`.
- **Hard to edit safely** — one giant file means each change needs the whole file
  in context, and there's no type checking to catch a renamed field.

A lightweight component framework with **scoped styles** and **TypeScript** fixes
both: each piece owns its markup + CSS, and the API shapes are typed.

## Framework choice — **Svelte 5 + Vite + TypeScript**
Recommended because it best fits this project's "small, no runtime bloat" ethos:
- **Compiles away** to tiny vanilla JS — no virtual-DOM runtime shipped.
- **Scoped `<style>` per component** — directly kills the global-CSS alignment drift.
- **Reactive stores** map cleanly onto today's globals (`CATALOG`, `TABS`,
  `RUNNING`, `SYSINFO`, `PERSONAS`, `ENSEMBLES`).
- Least boilerplate to read/write; first-class TS.

Alternatives if preferred: **SolidJS** (JSX, tiny, fine-grained reactivity) or
**Preact + signals** (React API in 3 KB). All meet the bar; Svelte is the pick.

> The Go server is **unchanged in spirit**: it still serves static files from
> `internal/server/web` via `go:embed`. The only difference is those files become
> the **`vite build` output** instead of hand-written. The shipped factory image
> stays self-contained — the Node build step is dev-time only.

## Repo layout
```
ui/                      NEW: frontend source (Svelte + TS) — distinct from the
                         served dir below; excluded from the Docker build context
  src/
    lib/
      api.ts             typed fetch client + SSE stream helper
      types.ts           Model, Image, Container, Persona, Ensemble, SysInfo …
      stores.ts          catalog, sysinfo, tabs, personas, ensembles (typed)
      icons.ts           the SVG set, typed
    components/          Modal, Slider, Toast, Tooltip, Table, Icon, PerfChips,
                         ModalityBadge, ModelPicker, IconButton …
    pages/               Build, Images, Containers, ModelFiles, Personas,
                         Ensembles, Chat (+ chat/* subcomponents), Help
    App.svelte           shell: sidebar nav, topbar, sys pill, collapse, router
    main.ts
  index.html             tiny Vite entry
  package.json / vite.config.ts / tsconfig.json
internal/server/web/     unchanged role: the BUILT output Go embeds (vite outDir)
```

## Dev workflow
- **Dev:** `vite dev` (HMR) on :5173, proxying `/api/*` and `/v1/*` to the Go
  server (`server.New(..., webDir="")` keeps serving its own API). Update the dev
  container/scripts to run vite alongside `wgo`, or run vite on the host against
  the containerized API.
- **Prod/build:** `vite build` writes to `internal/server/web`; `go build` embeds
  it as today. A `make ui` / npm script wires this. Commit the build output (or
  build in CI) so a plain `go build` still works without Node.

## Component breakdown (the monolith, split)
- **Shell:** `App` (sidebar + topbar + sys pill + collapse + hash router).
- **Primitives:** `Icon`, `IconButton`, `Modal`, `Slider`, `Toast`, `Tooltip`,
  `Table`, `Badge`, `PerfChips`, `ModalityBadge`.
- **Build page:** `ModelPicker` (searchable combo + filters), `ModelDetail`
  (caps/ratings), `ResourceTiers` (ctx/mem), `BuildForm`, `BuildProgress`.
- **Lists:** `ImagesTable` (+ `RunModal`), `ContainersTable`, `ModelFilesTable`.
- **Personas:** `PersonaList`, `PersonaEditor`.
- **Ensembles:** `EnsembleList`, `EnsembleEditor` (member checklist), build log.
- **Chat:** `ChatSidebar` (threads), `ChatToolbar`, `ChatLog`, `MessageBubble`
  (text/markdown/think/image/video/audio/bridge/scene), `Composer`,
  `SamplingPanel` (the sliders), `BridgeModal`.

## State + types
- A **`types.ts`** mirroring the Go structs (`catalog.Model`, `SysInfo`, persona,
  ensemble, container/image shapes). Hand-mirrored at this size; could later be
  generated from Go.
- **Typed stores** replace the globals; components subscribe instead of reading a
  module-level `let`. Chat persistence (localStorage) and bridge logic move into a
  `chat` store/module.

## Phased migration (incremental — Go API never changes)
- **P0 — Scaffold:** Vite + Svelte + TS; build → `internal/server/web`; confirm Go
  still embeds + serves it; wire dev proxy; a "hello" page served by Go.
- **P1 — Shell + plumbing:** `App` shell, router, theme (CSS vars), `api.ts`,
  `types.ts`, `icons.ts`, primitives (Icon/Modal/Toast/Tooltip/Table).
- **P2 — Simple pages:** Images, Containers, Model files, Personas, Help.
- **P3 — Build page:** model picker + filters + detail + resource tiers + progress.
- **P4 — Chat page:** tabs/threads, streaming, bubbles, sampling sliders, media.
- **P5 — Ensembles + modals:** EnsembleEditor, Run/Bridge modals.
- **P6 — Cutover:** delete the old `app.js`/`index.html`/`style.css`; fix the
  lingering alignment issues during componentization; polish.

During P1–P5 the old and new can coexist (build the new bundle into a subpath, or
just port page-by-page) since the **API is stable**.

## Risks / tradeoffs
- Adds a **Node/npm + Vite** dev dependency (deliverable image is unaffected).
- The **Docker-out-of-Docker dev setup** needs a small update for Vite HMR (P0).
- Chat (streaming + bridge + media) is the most intricate port — do it last (P4).
- It's a sizable effort; the win is every future change becomes a small, typed,
  scoped component edit instead of surgery on a 1,900-line file.

## Definition of done
Feature parity with today's UI, all pages componentized with scoped styles, typed
API + stores, `vite build` embedded by Go, the three vanilla web files deleted.
