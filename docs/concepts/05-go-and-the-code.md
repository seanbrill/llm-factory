# Go & the codebase

> **One-line version:** the factory is written in **Go**, a simple compiled language
> that's great for small, fast servers and command-line tools. This is a guided tour
> so you can find and change things.

## Go in 60 seconds

- **Compiled & single-binary:** `go build` turns the code into one self-contained
  executable. No interpreter or dependencies to install to *run* it.
- **Standard library is rich:** this project uses **only** the standard library (no
  third-party packages) — easy to audit, nothing to keep updated.
- **Packages = folders:** each folder under `internal/` is a package. `cmd/` holds
  programs (each `main.go` is an entry point).

To work on it you'd install Go from https://go.dev/dl/ — but you **don't need Go**
just to *use* the factory (the container ships the compiled binary).

Handy commands while developing:
```bash
go build ./...     # compile everything (catches errors)
go test ./...      # run the tests
gofmt -w .         # auto-format the code
go run ./cmd/builder -dir .   # run the control panel directly (no container)
```

## The two programs (`cmd/`)

- **`cmd/builder/`** — the **control panel**. This is what `start.sh` runs (inside a
  container). `main.go` wires up the catalog, builder, and HTTP server. *Start
  reading here.*
- **`cmd/llmgate/`** — a tiny **shim baked inside** each llama.cpp model image. It
  starts `llama-server` and acts as a reverse proxy that injects the baked system
  prompt. It is *not* part of the control panel; it's compiled into model images.

## The library (`internal/`)

- **`internal/builder/builder.go`** — the heart. If you want to change *how* models
  are built or run, it's here. Key functions:
  - `Build(...)` — the whole build flow (download → assemble context → `build`).
  - `Run(...)` — start a container with the right flags from its labels.
  - `runtimeFamily(...)` — modality → which runtime/Dockerfile.
  - `EnsureModel` / `EnsureMMProj` — download weights / vision projector.
  - `RegenProxy` / `RecoverOnStartup` — the Caddy proxy + reboot recovery.
  - `ClassifyEngineError` — turn raw engine errors into friendly messages.
- **`internal/server/server.go`** — the HTTP API. Each `/api/...` endpoint is a
  `handleX` function. It also serves the embedded web UI.
- **`internal/catalog/`** — the model list. The default is **embedded** from
  `models.default.json`; at runtime a user-editable copy lives in `config/models.json`.
  *To add a model, edit `models.default.json`* (and it appears on the next build of
  the factory image).

## The web UI (`internal/server/web/`)

Plain HTML/CSS/JS, no framework:
- **`index.html`** — the page structure (the panels you see).
- **`app.js`** — all the behavior: loads the catalog, builds the dropdowns and
  capability filter, renders the images/containers tables, and calls the `/api/...`
  endpoints. In dev mode you can hot-reload it (see below).
- **`style.css`** — styling, including the modality badges and "fits your system"
  colors.

The UI is **embedded** into the Go binary at build time, so changing it normally
needs a rebuild — *except* in dev mode.

## Dev mode (hot-reload, no rebuilds)

Run the factory pointing at the UI files on disk so edits show up on refresh:
```bash
./scripts/macos/start-dev.sh     # (or linux/ , windows/)
```
This mounts `internal/server/web/` into the container and serves it live. Great for
tweaking the UI.

## Where to make common changes

| You want to… | Edit |
|---|---|
| Add or update a model | `internal/catalog/models.default.json` |
| Change how a model is built | `docker/Dockerfile.<family>.<compute>` + `builder.Build` |
| Add a new API endpoint | `internal/server/server.go` (add a `handleX` + route) |
| Change the UI | `internal/server/web/{index.html,app.js,style.css}` |
| Change the in-image launch flags | `cmd/llmgate/main.go` (`llamaArgs`) |
| Add a new runtime (e.g. a new engine) | a new `docker/Dockerfile.<family>.*` + a case in `runtimeFamily` |
</content>
