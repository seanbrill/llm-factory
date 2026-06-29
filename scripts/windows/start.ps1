# Start the local-llm FACTORY (runs the factory itself in Docker — cross-platform,
# no Go toolchain needed on the host).
#
# The factory builds/runs model images via the host's Docker daemon
# (Docker-out-of-Docker), so the host docker socket is mounted. Downloaded
# weights, exported tarballs, and the model catalog are bind-mounted back to the
# host so they persist and stay visible in ./models, ./images, ./config.
#
#   .\start.ps1            # build + run, UI at http://localhost:8799
#   .\start.ps1 -Port 9000
#
# (For native dev without Docker you can still run:  go run ./cmd/builder)
#
# Note: we deliberately do NOT set $ErrorActionPreference = "Stop" — docker
# streams build progress to stderr, which Stop would turn into a fatal error.
# Real failures are caught via $LASTEXITCODE checks instead.

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

$name = "local-llm-factory"
New-Item -ItemType Directory -Force -Path .\models, .\images, .\config, .\media | Out-Null

# Build the Svelte UI into internal/server/web so the factory image embeds the
# latest UI. Runs in a throwaway Node container, so no host Node is required.
Write-Host "Building UI (Svelte -> internal/server/web)..." -ForegroundColor Cyan
docker run --rm -v "$($PWD.Path):/app" -w /app/ui node:20-alpine sh -c "npm install --no-audit --no-fund --silent && npm run build"
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: UI build failed." -ForegroundColor Red; exit 1 }

Write-Host "Building factory image ($name)..." -ForegroundColor Cyan
docker build -t $name -f Dockerfile.factory .
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: factory image build failed." -ForegroundColor Red; exit 1 }

# Remove any previous instance (guarded so we never `docker rm` a missing container).
if (docker ps -aq -f "name=^$name$") { docker rm -f $name | Out-Null }

# Optional Podman engine: mount a host Podman machine socket when present so the
# factory's "Podman" engine option works. Primarily the macOS GPU path; on Windows
# this is best-effort (Podman GPU is a macOS feature — use Docker+CUDA on Windows).
$podmanArgs = @()
if (Get-Command podman -ErrorAction SilentlyContinue) {
    $psock = (podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}' 2>$null | Select-Object -First 1)
    if ($psock) {
        $podmanArgs = @("-v", "$($psock):/run/podman/podman.sock", "-e", "CONTAINER_HOST=unix:///run/podman/podman.sock")
        Write-Host "Podman machine detected - enabling the Podman engine (socket: $psock)" -ForegroundColor Cyan
    }
}

# GPU hint for the UI's "fits your system" badges. On Windows the GPU path is
# Docker + CUDA (NVIDIA), so when an NVIDIA GPU is present we tell the factory to
# rate models for GPU. This only sets the UI hint (read as FACTORY_GPU in the
# container) - the model images themselves get `--gpus all` from the builder when
# Compute = CUDA. Best-effort: a detection hiccup must not abort startup, so the
# nvidia-smi failure is swallowed and we check $LASTEXITCODE rather than throwing.
$gpuArgs = @()
if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) {
    & nvidia-smi -L 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) {
        $gpuArgs = @("-e", "FACTORY_GPU=cuda")
        # Also pass total VRAM (GB) so the UI can flag models that won't fit.
        $vramMiB = (& nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>$null | Select-Object -First 1)
        $vramGB = 0; [double]::TryParse(($vramMiB -replace '[^\d.]', ''), [ref]$vramGB) | Out-Null
        $vramGB = [math]::Round($vramGB / 1024, 1)
        if ($vramGB -gt 0) { $gpuArgs += @("-e", "FACTORY_VRAM=$vramGB") }
        Write-Host "NVIDIA GPU detected ($vramGB GB) - models rated for CUDA GPU." -ForegroundColor Cyan
    }
}

Write-Host "Starting factory container..." -ForegroundColor Cyan
docker run -d --name $name `
    -p "$($Port):8799" `
    -e "HOST_DIR=$($PWD.Path -replace '\\','/')" `
    -v /var/run/docker.sock:/var/run/docker.sock `
    -v "$($PWD.Path)\models:/app/models" `
    -v "$($PWD.Path)\images:/app/images" `
    -v "$($PWD.Path)\config:/app/config" `
    -v "$($PWD.Path)\media:/app/media" `
    --add-host host.docker.internal:host-gateway `
    $gpuArgs `
    $podmanArgs `
    $name
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: failed to start factory container." -ForegroundColor Red; exit 1 }

Write-Host "local-llm factory is running at http://localhost:$Port" -ForegroundColor Green
Write-Host "Logs:  docker logs -f $name"
Write-Host "Stop:  .\stop.ps1"

} finally {
    Pop-Location   # restore the caller's directory regardless of how we exit
}
