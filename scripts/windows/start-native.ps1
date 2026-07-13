# Start the local-llm FACTORY as a NATIVE host process (not in Docker).
#
# Running natively lets the factory shell out to the host's podman/docker CLIs
# directly, so the Containers tab sees and manages model containers even when a
# containerized factory can't reach the engine socket. Mirrors how protoTree talks
# to the engines.
#
#   ./start-native.ps1
#   $env:PORT="9000"; ./start-native.ps1
#   $env:OPEN="0"; ./start-native.ps1     # don't auto-open the browser
#
# Stop with Ctrl-C.
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")

if (-not (Get-Command go -ErrorAction SilentlyContinue)) { Write-Error "Go is required."; exit 1 }

$port = if ($env:PORT) { $env:PORT } else { "8799" }
New-Item -ItemType Directory -Force -Path models, images, config | Out-Null

Write-Host "Building factory (native)..."
go build -o local-llm.exe ./cmd/builder   # UI is embedded from internal/server/web (committed)

if (Get-Command podman -ErrorAction SilentlyContinue) {
  Write-Host "podman: $(podman --version)"
} else {
  Write-Warning "podman not on PATH — install it if the Containers tab is empty (docker still works)."
}

Write-Host "Factory (native — sees host podman + docker): http://127.0.0.1:$port   (Ctrl-C to stop)"
if (($env:OPEN -ne "0")) { Start-Process "http://127.0.0.1:$port" }

& .\local-llm.exe -addr "127.0.0.1:$port"
