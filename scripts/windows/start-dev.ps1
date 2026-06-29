# Start the factory in DEV mode with independent hot-reload:
#   • Backend: wgo rebuilds/restarts the Go server when *.go files change.
#   • UI:      served from ./internal/server/web on disk — edit + refresh, no rebuild.
#
# The whole repo is bind-mounted into the container, so host edits are live.
# This does NOT rebuild the image on code changes (only the first time, to get
# the Go toolchain + wgo + docker CLI).
#
#   .\start-dev.ps1            # UI at http://localhost:8799, hot-reloads
#   .\start-dev.ps1 -Port 9000
#
# Stop with .\stop.ps1 (removes prod and dev containers).

param([int]$Port = 8799)

# Docker's build context and the bind-mount paths are repo-relative, so the body
# runs from the repo root. We use Push/Pop (not Set-Location) so invoking this
# from scripts\windows leaves the caller's directory unchanged. The finally below
# restores it on every exit path, including the `exit 1` early-returns.
Push-Location -Path (Join-Path $PSScriptRoot '..\..')
try {

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: Docker is required and must be running." -ForegroundColor Red
    exit 1
}

$name = "local-llm-factory-dev"
New-Item -ItemType Directory -Force -Path .\models, .\images, .\config | Out-Null

# Build the Svelte UI once into internal/server/web (the dev container serves it
# from disk via -web), then keep it rebuilding on source edits below.
Write-Host "Building UI (Svelte -> internal/server/web)..." -ForegroundColor Cyan
docker run --rm -v "$($PWD.Path):/app" -w /app/ui node:20-alpine sh -c "npm install --no-audit --no-fund --silent && npm run build"
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: UI build failed." -ForegroundColor Red; exit 1 }

# UI hot-reload: a background `vite build --watch` rebuilds internal/server/web on
# every ui/src edit; the dev container serves the fresh bundle from disk, so you
# just refresh the browser (the Go backend hot-reloads separately via wgo).
# CHOKIDAR_USEPOLLING is REQUIRED — inotify events don't cross Docker Desktop
# bind mounts (the same reason Dockerfile.dev runs wgo with -poll). The watch
# container is named so stop.ps1 removes it; reuse node_modules from the build above.
docker rm -f local-llm-ui-watch 2>$null | Out-Null
docker run -d --name local-llm-ui-watch -e CHOKIDAR_USEPOLLING=true `
    -v "$($PWD.Path):/app" -w /app/ui node:20-alpine `
    sh -c "npm run build -- --watch" | Out-Null
Write-Host "UI watch on: edit ui/src/* -> auto-rebuild -> refresh the browser." -ForegroundColor Cyan

Write-Host "Building dev image ($name)..." -ForegroundColor Cyan
docker build -t $name -f Dockerfile.dev .
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: dev image build failed." -ForegroundColor Red; exit 1 }

if (docker ps -aq -f "name=^$name$") { docker rm -f $name | Out-Null }

# Optional Podman engine (parity with start.ps1); best-effort on Windows.
$podmanArgs = @()
if (Get-Command podman -ErrorAction SilentlyContinue) {
    $psock = (podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}' 2>$null | Select-Object -First 1)
    if ($psock) {
        $podmanArgs = @("-v", "$($psock):/run/podman/podman.sock", "-e", "CONTAINER_HOST=unix:///run/podman/podman.sock")
        Write-Host "Podman machine detected - enabling the Podman engine (socket: $psock)" -ForegroundColor Cyan
    }
}

# GPU hint for the UI's "fits your system" badges (parity with start.ps1). On
# Windows the GPU path is Docker + CUDA, so flag an NVIDIA GPU to the factory.
# UI hint only; model images get `--gpus all` from the builder at Compute = CUDA.
$gpuArgs = @()
if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) {
    & nvidia-smi -L 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) {
        $gpuArgs = @("-e", "FACTORY_GPU=cuda")
        $vramMiB = (& nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>$null | Select-Object -First 1)
        $vramGB = 0; [double]::TryParse(($vramMiB -replace '[^\d.]', ''), [ref]$vramGB) | Out-Null
        $vramGB = [math]::Round($vramGB / 1024, 1)
        if ($vramGB -gt 0) { $gpuArgs += @("-e", "FACTORY_VRAM=$vramGB") }
        Write-Host "NVIDIA GPU detected ($vramGB GB) - models rated for CUDA GPU." -ForegroundColor Cyan
    }
}

Write-Host "Starting dev container (hot-reload)..." -ForegroundColor Cyan
docker run -d --name $name `
    -p "$($Port):8799" `
    -e "HOST_DIR=$($PWD.Path -replace '\\','/')" `
    -v /var/run/docker.sock:/var/run/docker.sock `
    -v "$($PWD.Path):/app" `
    -v local-llm-gocache:/root/.cache/go-build `
    --add-host host.docker.internal:host-gateway `
    $gpuArgs `
    $podmanArgs `
    $name
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: failed to start dev container." -ForegroundColor Red; exit 1 }

Write-Host "Dev factory running at http://localhost:$Port (hot-reload on)" -ForegroundColor Green
Write-Host "  UI:      edit ui/src/* -> auto-rebuild (vite watch) -> refresh the browser"
Write-Host "  Backend: edit *.go -> auto rebuild/restart (watch: docker logs -f $name)"
Write-Host "Stop:  .\stop.ps1"

} finally {
    Pop-Location   # restore the caller's directory regardless of how we exit
}
