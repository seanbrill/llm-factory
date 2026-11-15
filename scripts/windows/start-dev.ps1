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
#   .\start-dev.ps1 -Bind 0.0.0.0   # also reachable from your network (default:
#                                   # this computer only)
#
# The factory's API needs its access token (config\factory-token, saved on
# first start); the "Open:" link printed at the end carries it.
#
# Stop with .\stop.ps1 (removes prod and dev containers).

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

$name = "local-llm-factory-dev"
New-Item -ItemType Directory -Force -Path .\models, .\images, .\config, .\workspace | Out-Null

# Build the Svelte UI once into internal/server/web (the dev container serves it
# from disk via -web), then keep it rebuilding on source edits below.
Write-Host "Building UI (Svelte -> internal/server/web)..." -ForegroundColor Cyan
docker run --rm -v "$($PWD.Path):/app" -w /app/ui node:20-alpine sh -c "npm install --no-audit --no-fund --silent && npm run build"
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: UI build failed." -ForegroundColor Red; exit 1 }

# The FileFreak archive helper (docs/FILEFREAK.md): its SDK goes into
# tools/filefreak/node_modules, which the dev container sees through the repo mount.
Write-Host "Installing the FileFreak archive helper (tools/filefreak)..." -ForegroundColor Cyan
docker run --rm -v "$($PWD.Path):/app" -w /app/tools/filefreak node:20-alpine sh -c "npm ci --omit=dev --no-audit --no-fund --silent"
if ($LASTEXITCODE -ne 0) { Write-Host "WARN: npm ci in tools/filefreak failed, so the FileFreak archive is off: model files download from Hugging Face." -ForegroundColor Yellow }

# UI hot-reload: a background `vite build --watch` rebuilds internal/server/web on
# every ui/src edit; the dev container serves the fresh bundle from disk, so you
# just refresh the browser (the Go backend hot-reloads separately via wgo).
# CHOKIDAR_USEPOLLING is REQUIRED — inotify events don't cross Docker Desktop
# bind mounts (the same reason Dockerfile.dev runs wgo with -poll). The watch
# container is named so stop.ps1 removes it; reuse node_modules from the build above.
docker rm -f local-llm-ui-watch 2>$null | Out-Null
docker run -d --name local-llm-ui-watch -e CHOKIDAR_USEPOLLING=true -e VITE_WATCH=1 `
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
# HOST_HOME / FACTORY_PORT serve the agent harness (Harness page); the repo
# mount already provides /app/skills and /app/workspace.
docker run -d --name $name `
    -p "$($Bind):$($Port):8799" `
    -e "HOST_DIR=$($PWD.Path -replace '\\','/')" `
    -e "HOST_HOME=$($env:USERPROFILE -replace '\\','/')" `
    -e "FACTORY_PORT=$Port" `
    -e FILEFREAK_ARCHIVE_URL `
    -v /var/run/docker.sock:/var/run/docker.sock `
    -v "$($PWD.Path):/app" `
    -v local-llm-gocache:/root/.cache/go-build `
    --add-host host.docker.internal:host-gateway `
    $gpuArgs `
    $podmanArgs `
    $name
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: failed to start dev container." -ForegroundColor Red; exit 1 }

Write-Host "Dev factory running at http://localhost:$Port (hot-reload on)" -ForegroundColor Green
# The access link: wait (up to ~20 s on a first start) for the factory to save
# its token, then print the UI's address with it. The UI is served on $Port
# too (vite build --watch writes it to disk), so that's the port to open.
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
    Write-Host "Open: the access link is in the factory log (docker logs $name); config\factory-token didn't appear yet (the first dev start compiles the factory)." -ForegroundColor Yellow
}
if ($Bind -ne "127.0.0.1") { Write-Host "WARNING: listening on $($Bind):$Port - other machines can reach the factory. They still need its access token, but plain HTTP sends it unencrypted: do this only on a network you trust." -ForegroundColor Yellow }
Write-Host "  UI:      edit ui/src/* -> auto-rebuild (vite watch) -> refresh the browser"
Write-Host "  Backend: edit *.go -> auto rebuild/restart (watch: docker logs -f $name)"
Write-Host "Stop:  .\stop.ps1"

} finally {
    Pop-Location   # restore the caller's directory regardless of how we exit
}
