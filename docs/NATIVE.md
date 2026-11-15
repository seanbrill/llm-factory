# Native packages

A **native package** is one of your models, with its settings and its agent
harness, as a single zip for one operating system. Unzipped, it runs on that
computer **without Docker or Podman**: a small launcher starts llama.cpp's own
prebuilt server for that system, serves a chat page and the OpenAI API on
`localhost`, and runs the model's tools. On a Mac with Apple Silicon the model
runs on the GPU through Metal, at full speed.

Use one to hand a model to someone who doesn't run the factory, to run a model
on a laptop at full GPU speed, or to keep a model working on a machine without
a container engine.

| System | GPU | Status |
|---|---|---|
| macOS 13.3+ on Apple Silicon (M1 or later) | Metal, every layer on the GPU | Built first; packaging is tested, a run on a Mac still needs a person |
| Linux x86-64, glibc 2.34+ (Ubuntu 22.04, Debian 12, Fedora 35 or newer) | Vulkan when a driver is present, else CPU | Preview: packaged the same way, not yet run on such a machine |
| Windows 10/11 x86-64 | Vulkan when a driver is present, else CPU | Preview: packaged the same way, not yet run on such a machine |

Packages carry **llama.cpp chat models**: text, code, reasoning and vision
models from the catalog. Embedding models, image, video and speech models, and
Python-runtime models stay container-only for now (see "Later: ensembles").

## Making one

In the **Library**, open a model's row (the `…` button) and click **Native
package** on its image. The dialog is filled in from the image's build
settings: system prompt, context size and baked tools. A model with only
weights on disk (no image yet) gets the catalog's defaults. Pick the system,
check the settings, and click **Make package**. The dialog shows the log; you
can close it, and the Library's **Native packages** card lists the job until
it's done, then the zip with its **Download** button.

What the factory does:

1. Uses the weights in `models/` (downloading them first, as a build would).
2. Downloads llama.cpp's prebuilt release for the system from GitHub, once,
   into `packages/.cache/llama.cpp/<build>/`, checks it against the SHA-256 the
   factory pins (a mismatch is deleted and the package fails), and keeps only
   `llama-server`, its libraries and the license files.
3. Takes the launcher for the system: the factory image cross-compiles
   `cmd/llmpack` for every system when it is built (`/app/native/launchers/`);
   a factory run from source (`start-dev`, `start-native`) cross-compiles it
   with the Go toolchain instead.
4. Reads the model's license and notice files (`LICENSE*`, `NOTICE*`, the model
   card) from its Hugging Face repository, at the revision of the weights.
5. Writes `packages/<name>.zip`, with `packages/<name>.zip.sha256` and a
   `packages/<name>.json` description beside it.

Packages are made one at a time, on a queue of their own: packaging never waits
behind an image build, or holds one up. The start scripts mount `./packages`
into the factory, so the zips are in the factory folder on your computer too.

### The API

| Call | What it does |
|---|---|
| `GET /api/native/targets` | The systems, with the pinned llama.cpp build |
| `GET /api/native/source?ref=&engine=` or `?model_id=` | What a package of that image (or catalog entry) carries by default |
| `POST /api/native/package` | Queue a package: `{"ref", "engine"}` or `{"model_id"}`, `"target"`, and optionally `"name"`, `"system_prompt"`, `"inject_mode"`, `"ctx_size"`, `"harness"` (a harness.json object, `true`, or `false`). Settings left out come from the image. Returns `{id, status, position, package}` |
| `GET /api/native/jobs`, `GET /api/native/state?id=&offset=` | Jobs, and one job's log from an offset (like `/api/builds/state`) |
| `POST /api/native/cancel {"id"}` | Stop a job |
| `GET /api/native/packages` | The finished packages |
| `GET /api/native/download?name=` | The zip (supports ranges, so a browser can resume) |
| `POST /api/native/delete {"name"}` | Delete a package |

## What's in a package

```
qwen3.5-4b-macos-arm64/
  Start.command            double-click to start (Start.bat on Windows, start.sh on Linux)
  README.txt               how to run it, options, memory, licenses
  manifest.json            what the launcher runs: the model, its settings, the engine
  SHA256SUMS               the SHA-256 of every file (shasum -a 256 -c / sha256sum -c)
  bin/llmpack              the launcher (llmpack.exe on Windows)
  bin/llama/               llama-server and its libraries (llama.cpp v0.9.0, build b12110)
  model/<model>.gguf       the weights, stored uncompressed
  model/<mmproj>.gguf      the vision projector, for vision models
  config/system_prompt.txt the initialization prompt, when there is one
  harness/harness.json     the agent harness: tools and limits, when there is one
  harness/skills/          skills the model can read (when the skills tool is on)
  workspace/               the folder the file tools work in
  licenses/model/          the model's license files and model card
  licenses/llama.cpp/      llama.cpp's license
```

Everything sits in one top folder, so unzipping gives one folder. The weights
are stored, not compressed (GGUF weights don't compress), last in the archive.
The zip uses ZIP64 records for files over 4 GiB, with the sizes in every local
header and no data descriptors, so macOS Archive Utility and `ditto`, `unzip`,
`bsdtar` and Windows Explorer all read it, front to back or from the end. It
keeps Unix permissions (the programs stay executable) and llama.cpp's library
symlinks.

**Why the weights are separate files.** A Windows executable can't be larger
than 4 GB (the PE format's image size is 32 bits), and most models are bigger
than that, so "one .exe with the model inside" isn't possible there; on every
system llama.cpp memory-maps the weights from a file anyway. A folder with a
launcher, the engine and the weights works the same way everywhere.

### The llama.cpp build

Packages carry llama.cpp **v0.9.0**, which llama.cpp publishes as prebuilt
release **b12110**: the same commit
(`cd959bae99ff4bed22bef2604f45241c09bbb8c8`) the model images build from
source (`LLAMA_CPP_REF` in `scripts/docker/runtimes/Dockerfile.*`). A package and a container
built by the same factory run the same llama.cpp, with the same chat templates
and flags. The assets and their SHA-256s are pinned in
`internal/native/targets.go`:

| System | Asset |
|---|---|
| macOS arm64 | `llama-b12110-bin-macos-arm64.tar.gz` (Metal, ad-hoc signed by llama.cpp's CI, macOS 13.3+) |
| Linux x64 | `llama-b12110-bin-ubuntu-vulkan-x64.tar.gz` (Vulkan, with CPU backends for each CPU family) |
| Windows x64 | `llama-b12110-bin-win-vulkan-x64.zip` (Vulkan, with CPU backends) |

When the images move to another llama.cpp release, move these with them: the
release's `nightly-tag.txt` names the build of a `vX.Y.Z` tag, and GitHub lists
each asset's SHA-256.

## Running one

Unzip it, then:

- **macOS**: double-click `Start.command`. Terminal opens with the model's log
  and the browser opens the chat page. Close the Terminal window to stop it.
  The first time, see "Signing" below.
- **Windows**: right-click the zip, **Extract All**, then double-click
  `Start.bat`. A console window shows the log. SmartScreen may warn the first
  time: **More info**, then **Run anyway**.
- **Linux**: `./start.sh` in a terminal.

The chat page is at `http://127.0.0.1:8080` (the next free port when 8080 is
taken; the log says which), and the OpenAI-compatible API at
`http://127.0.0.1:8080/v1`, with no key. Any OpenAI client works:

```sh
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"Hello"}]}'
```

`GET /v1/package` describes the package, and `GET /v1/harness` the tools, as a
model image does.

Launcher options (`./bin/llmpack --help`; pass them to the start script too):

| Option | |
|---|---|
| `--port 9000` | another port (when set, it's that port or an error) |
| `--ctx-size 16384` | another context window |
| `--gpu-layers all\|auto\|N` | layers on the GPU; `0` runs on the CPU |
| `--threads N` | CPU threads (default: llama.cpp's choice) |
| `--no-browser` | don't open the chat page |
| `--no-harness` | the plain model, without its tools |
| `--workspace DIR` | another folder for the model's files |
| `--host 0.0.0.0` | listen on the network (see "Security") |
| `--allow-origin URL` | let a web page on that origin call the API from a browser |
| `--quiet` | llama-server's log to `llama-server.log` |
| `--verify` | check every file against `SHA256SUMS`, then exit |
| `-- ARGS` | anything after `--` goes to llama-server unchanged |

The harness reads the same `HARNESS_*` variables as in a container
(`HARNESS=off`, `HARNESS_TOOLS=-web_search`, `HARNESS_BRAVE_API_KEY`,
`HARNESS_SEARXNG_URL`, ...), except the workspace and skills folders, which the
package and `--workspace` decide.

## Speed and memory, compared with containers

- **Mac**: a container can't use the Mac's GPU directly. In Docker Desktop the
  model runs on the CPU inside a Linux VM; with the factory's Podman (libkrun)
  machine it reaches the GPU through Vulkan forwarded into the VM (Venus),
  which works but is slower than Metal and has open bugs (the factory keeps
  vision projectors on the CPU there). A native package runs llama.cpp's Metal
  build directly, with every layer on the GPU, which is how llama.cpp is meant
  to run on Apple Silicon. Memory: there's no VM with a fixed memory size in
  between, so the model can use the Mac's memory up to what macOS lets the GPU
  take, about two-thirds of it (three-quarters above 36 GB). The README in each
  package says how to fit a model that's too big (`--ctx-size`,
  `--gpu-layers auto`).
- **Linux**: containers already reach the GPU directly (CUDA or Vulkan device
  passthrough), so a native package runs at about the same speed. It's for
  machines without Docker or Podman.
- **Windows**: the factory's containers use the GPU only through NVIDIA's CUDA
  in WSL 2, and run on the CPU otherwise. The native package's Vulkan build
  uses any GPU with a Vulkan driver (NVIDIA, AMD or Intel).

The first start reads the whole model from disk; the log says when it's loaded
("model loaded in ...: ready"), and the chat page shows it.

## Security model

A container keeps a model and its tools away from the rest of the computer. A
native package has no such wall: the launcher, llama-server and the tools run
as you, with your permissions. So a package does less, on purpose:

- **No shell tool, ever.** The factory leaves it out of the package's
  `harness.json`, and the launcher removes it again at start, whatever the file
  or `HARNESS_TOOLS` say. Only the factory's containers offer it.
- **File tools stay in one folder**: the package's `workspace` folder (or
  `--workspace`). The harness refuses paths outside it, `..` and links that
  lead out included (internal/agent's workspace checks).
- **Page reading can't reach your machine or network**: `fetch_url` refuses
  loopback, private and link-local addresses, as in a container. Web search
  sends the model's queries to the search engine (DuckDuckGo by default).
- **Only this computer can connect.** The launcher listens on `127.0.0.1`.
  llama-server listens on a random loopback port behind it, with a random API
  key that only the launcher knows (passed in its environment, so it isn't on
  the command line or in the log): calling llama-server directly gets 401.
- **Other web pages can't use it.** A page on any site can make your browser
  send requests to `127.0.0.1`, so the launcher refuses requests whose `Origin`
  isn't its own chat page (or one you allow with `--allow-origin`), and
  requests whose `Host` isn't `127.0.0.1`/`localhost` (which stops DNS
  rebinding). Programs that aren't browsers send no `Origin` and work as usual.
- `--host 0.0.0.0` shares the model **and its tools** with everyone who can
  reach your computer, with no key. The launcher warns when you use it.

What it doesn't protect against: other programs on this computer can call the
launcher's API, as with any local server without a key; and the model can
write whatever it likes into its workspace folder.

`SHA256SUMS` lists every file; `--verify` (or `shasum -a 256 -c SHA256SUMS`)
checks them after a download. The zip's own SHA-256 is in the factory's log and
in `packages/<name>.zip.sha256`.

## Signing

Nothing in a package is signed with a publisher's certificate, so each system
warns once:

- **macOS**: the launcher is ad-hoc signed by Go's linker (Apple Silicon runs
  no unsigned code at all) and llama.cpp's binaries come ad-hoc signed from its
  CI. That's enough to run, but not for Gatekeeper: a zip downloaded through a
  browser is quarantined, and macOS says it can't check `Start.command`. Click
  Done, then **System Settings → Privacy & Security → Open Anyway**, and
  double-click it again; `Start.command` then clears the quarantine flag from
  the package folder so the launcher and llama.cpp run. (In Terminal:
  `xattr -dr com.apple.quarantine <folder>`.) A zip copied straight from the
  factory's `packages/` folder isn't quarantined and doesn't ask.
  To remove the warning for everyone, the package would need a **Developer ID**
  signature and Apple's **notarization**: an Apple Developer Program membership,
  every binary signed with the hardened runtime, and the result submitted with
  `notarytool`. A ticket can't be stapled to a zip or a `.command`, so a
  notarized package would ship as a signed `.app` inside a `.dmg`. The factory
  doesn't do this (it has no Apple identity); it's the step to add for public
  distribution.
- **Windows**: SmartScreen warns about the unsigned launcher (**More info →
  Run anyway**). Unblocking the zip (right-click, Properties, **Unblock**)
  before extracting avoids it for every file. An Authenticode signature on
  the launcher, once its certificate has a reputation with SmartScreen,
  removes it.
- **Linux**: nothing asks. Unzip with a tool that keeps permissions.

The start script is a `.command` rather than an `.app` on purpose: an app
bundle is checked as a whole, and one with an ad-hoc-signed program inside an
unsigned bundle reads as "damaged" once downloaded, with no Open Anyway.

## How it's built (for developers)

| Code | |
|---|---|
| `cmd/llmpack` | The launcher. Pure Go (`CGO_ENABLED=0`), cross-compiled for every system. Reads `manifest.json`, starts llama-server with the system's flags, serves the chat page (`chat.html`, embedded), `/v1/package` and the gate. |
| `internal/gate` | The front door shared with `cmd/llmgate` (the model images' gate): system-prompt injection, the harness's tool loop, the proxy to llama-server, and llama-server's command line (`gate.Llama`). |
| `internal/native` | Targets and the pinned llama.cpp assets, the manifest, `SHA256SUMS`, and the process supervisor. |
| `internal/builder/native*.go` | Making a package: the llama.cpp cache (`native_runtime.go`), the launcher (`native_launcher.go`), license files (`native_license.go`), README and start scripts (`native_readme.go`), the ZIP64 writer (`native_zip.go`). |
| `internal/server/native.go` | `/api/native/*` and the packaging queue. |
| `ui/src/components/library/NativeDialog.svelte`, `NativePackages.svelte`, `ui/src/lib/native.ts` | The Library's dialog and card. |

Go's linker signs darwin/arm64 programs ad hoc whatever system it runs on
(`cmd/link`: code signing is needed for darwin/arm64), so the launcher the
Linux factory image builds runs on a Mac; the builder checks the Mach-O has its
`LC_CODE_SIGNATURE` before packing it, and the tests cross-compile it and check
the same. `codesign -dv bin/llmpack` on a Mac shows `Signature=adhoc`,
`flags=0x20002(adhoc,linker-signed)`.

Adding a system is an entry in `internal/native/targets.go` (its llama.cpp
asset and SHA-256), a launcher in the `scripts/docker/Dockerfile.factory` loop, and its start
script and README text in `native_readme.go`.

## Later: ensembles

The manifest lists **engines by role** (only `chat` today), and the launcher's
supervisor already runs several processes as one unit: when one stops, the
others are stopped too. An ensemble package would add engines that have native
builds of their own: **stable-diffusion.cpp** (`sd-server`) for images,
**whisper.cpp** (`whisper-server`) for speech to text, and llama.cpp for the
rest, with the gate routing each request to its engine, as `cmd/ensemblegate`
does in a container. Models that need the **Python runtime** (PyTorch,
ComfyUI) stay container-only: they need a Python environment and, mostly,
CUDA, which a zip can't carry.

## Needs a real run

The packaging, the archive format, the launcher's flags and checks are covered
by tests (`go test ./internal/native/ ./internal/builder/ ./cmd/llmpack/
./internal/server/`). What only a person can check, on each system:

- the package unzips with the system's own tool and the start script runs;
- the first-start warnings and their fixes (Gatekeeper, SmartScreen) read as
  this page says;
- llama-server starts, the model loads on the GPU (Metal: the log lists the
  GPU and "offloaded N/N layers"), the chat page answers and the tools work;
- `--verify` passes on the unzipped folder.
