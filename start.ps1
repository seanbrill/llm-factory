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

Set-Location -Path $PSScriptRoot

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: Docker is required and must be running." -ForegroundColor Red
    exit 1
}

$name = "local-llm-factory"
New-Item -ItemType Directory -Force -Path .\models, .\images, .\config | Out-Null

Write-Host "Building factory image ($name)..." -ForegroundColor Cyan
docker build -t $name -f Dockerfile.factory .
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: factory image build failed." -ForegroundColor Red; exit 1 }

# Remove any previous instance (guarded so we never `docker rm` a missing container).
if (docker ps -aq -f "name=^$name$") { docker rm -f $name | Out-Null }

Write-Host "Starting factory container..." -ForegroundColor Cyan
docker run -d --name $name `
    -p "$($Port):8799" `
    -e "HOST_DIR=$($PWD.Path -replace '\\','/')" `
    -v /var/run/docker.sock:/var/run/docker.sock `
    -v "$($PWD.Path)\models:/app/models" `
    -v "$($PWD.Path)\images:/app/images" `
    -v "$($PWD.Path)\config:/app/config" `
    --add-host host.docker.internal:host-gateway `
    $name
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: failed to start factory container." -ForegroundColor Red; exit 1 }

Write-Host "local-llm factory is running at http://localhost:$Port" -ForegroundColor Green
Write-Host "Logs:  docker logs -f $name"
Write-Host "Stop:  .\stop.ps1"
