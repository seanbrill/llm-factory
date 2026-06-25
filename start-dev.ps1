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

Set-Location -Path $PSScriptRoot

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: Docker is required and must be running." -ForegroundColor Red
    exit 1
}

$name = "local-llm-factory-dev"
New-Item -ItemType Directory -Force -Path .\models, .\images, .\config | Out-Null

Write-Host "Building dev image ($name)..." -ForegroundColor Cyan
docker build -t $name -f Dockerfile.dev .
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: dev image build failed." -ForegroundColor Red; exit 1 }

if (docker ps -aq -f "name=^$name$") { docker rm -f $name | Out-Null }

Write-Host "Starting dev container (hot-reload)..." -ForegroundColor Cyan
docker run -d --name $name `
    -p "$($Port):8799" `
    -e "HOST_DIR=$($PWD.Path -replace '\\','/')" `
    -v /var/run/docker.sock:/var/run/docker.sock `
    -v "$($PWD.Path):/app" `
    -v local-llm-gocache:/root/.cache/go-build `
    --add-host host.docker.internal:host-gateway `
    $name
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: failed to start dev container." -ForegroundColor Red; exit 1 }

Write-Host "Dev factory running at http://localhost:$Port (hot-reload on)" -ForegroundColor Green
Write-Host "  UI:      edit internal/server/web/* -> refresh the browser"
Write-Host "  Backend: edit *.go -> auto rebuild/restart (watch: docker logs -f $name)"
Write-Host "Stop:  .\stop.ps1"
