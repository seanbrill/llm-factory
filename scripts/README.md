# scripts

Everything you run to start and stop the factory, by platform, plus the
Dockerfiles. Run a script from anywhere: it moves to the repo root first.

| Folder | What's in it |
|---|---|
| `macos/` | bash scripts for macOS |
| `linux/` | bash scripts for Linux |
| `windows/` | the same scripts in PowerShell (`.ps1`) |
| `docker/` | `Dockerfile.factory` (the factory's own image), `Dockerfile.dev` (its hot-reload image) |
| `docker/runtimes/` | the Dockerfiles the factory builds model and ensemble images from, one per runtime and compute (`Dockerfile.vulkan`, `Dockerfile.sd.vulkan`, `Dockerfile.ensemble`, …) |

Each platform folder has the same scripts:

| Script | What it does |
|---|---|
| `start` | Builds the UI and the factory image, then runs the factory in Docker. The usual way to start it. |
| `start-dev` | Runs the factory with the repo mounted and hot reload for the Go backend and the UI. |
| `start-native` | Runs the factory as a plain process on this computer (no container). |
| `stop` | Stops the factory container. Model containers keep running; stop those in the UI. |
| `free-memory` | Drops the Docker VM's page cache without stopping containers. |
| `version` | Sourced by the start scripts to stamp the build's version. Not run on its own. |

```bash
./scripts/macos/start.sh        # macOS
./scripts/linux/start.sh        # Linux
```
```powershell
.\scripts\windows\start.ps1     # Windows
```

Both Docker builds use the repo root as the context, for example
`docker build -f scripts/docker/Dockerfile.factory .`.

The factory reads `docker/runtimes/` each time it builds a model. With
`start-dev` or `start-native` it reads them from the repo, so an edit applies to
the next model build. `start` copies them into the factory image, so there an
edit applies after you run `start` again.
