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
#   .\start.ps1 -Bind 0.0.0.0   # also reachable from your network (default: this
#                               # computer only)
#
# The factory's API needs its access token, which the factory saves in
# config\factory-token on first start. The "Open:" link printed at the end
# carries it and signs your browser in.
#
# (For native dev without Docker you can still run:  go run ./cmd/builder)
#
# Note: we deliberately do NOT set $ErrorActionPreference = "Stop" — docker
# streams build progress to stderr, which Stop would turn into a fatal error.
# Real failures are caught via $LASTEXITCODE checks instead.

param([int]$Port = 8799, [string]$Bind = "127.0.0.1")

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
New-Item -ItemType Directory -Force -Path .\models, .\images, .\config, .\media, .\workspace | Out-Null

# Stamp the build with its version (shown in the UI sidebar). Computed before
# the UI build so "-dirty" reflects only your own local edits.
. (Join-Path $PSScriptRoot 'version.ps1')

# Build the Svelte UI into internal/server/web so the factory image embeds the
# latest UI. Runs in a throwaway Node container, so no host Node is required.
Write-Host "Building UI (Svelte -> internal/server/web)..." -ForegroundColor Cyan
docker run --rm -v "$($PWD.Path):/app" -w /app/ui node:20-alpine sh -c "npm install --no-audit --no-fund --silent && npm run build"
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: UI build failed." -ForegroundColor Red; exit 1 }

Write-Host "Building factory image ($name) v$FactoryVersion ($FactoryCommit)..." -ForegroundColor Cyan
docker build --build-arg "FACTORY_VERSION=$FactoryVersion" --build-arg "FACTORY_COMMIT=$FactoryCommit" -t $name -f Dockerfile.factory .
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
# HOST_HOME / FACTORY_PORT and the skills + workspace mounts serve the agent
# harness (Harness page): its workspace terminal mounts host paths, and Open
# WebUI reaches the model gateway on the published port.
docker run -d --name $name `
    -p "$($Bind):$($Port):8799" `
    -e "HOST_DIR=$($PWD.Path -replace '\\','/')" `
    -e "HOST_HOME=$($env:USERPROFILE -replace '\\','/')" `
    -e "FACTORY_PORT=$Port" `
    -e FILEFREAK_ARCHIVE_URL `
    -v /var/run/docker.sock:/var/run/docker.sock `
    -v "$($PWD.Path)\models:/app/models" `
    -v "$($PWD.Path)\images:/app/images" `
    -v "$($PWD.Path)\config:/app/config" `
    -v "$($PWD.Path)\media:/app/media" `
    -v "$($PWD.Path)\skills:/app/skills:ro" `
    -v "$($PWD.Path)\workspace:/app/workspace" `
    --add-host host.docker.internal:host-gateway `
    $gpuArgs `
    $podmanArgs `
    $name
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: failed to start factory container." -ForegroundColor Red; exit 1 }

Write-Host "local-llm factory is running at http://localhost:$Port" -ForegroundColor Green
# The access link: wait (up to ~20 s on a first start) for the factory to save
# its token, then print the UI's address with it.
$tokenFile = Join-Path $PWD.Path 'config\factory-token'
$token = ""
for ($i = 0; $i -lt 40; $i++) {
    $raw = Get-Content -LiteralPath $tokenFile -Raw -ErrorAction SilentlyContinue
    if ($raw) { $token = ([string]$raw).Trim() }
    if ($token.Length -ge 24) { break }
    $token = ""
    Start-Sleep -Milliseconds 500
}
if ($token) {
    # Escaped for the link: a generated token is hex, but one you wrote into
    # the file yourself may hold characters like + & # %.
    Write-Host "Open: http://localhost:$Port/?token=$([uri]::EscapeDataString($token))" -ForegroundColor Green
} else {
    Write-Host "Open: the access link is in the factory log (docker logs $name); config\factory-token didn't appear." -ForegroundColor Yellow
}
if ($Bind -ne "127.0.0.1") { Write-Host "WARNING: listening on $($Bind):$Port - other machines can reach the factory. They still need its access token, but plain HTTP sends it unencrypted: do this only on a network you trust." -ForegroundColor Yellow }
Write-Host "Logs:  docker logs -f $name"
Write-Host "Stop:  .\stop.ps1"

} finally {
    Pop-Location   # restore the caller's directory regardless of how we exit
}
