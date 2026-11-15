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
# The factory's API needs its access token (config\factory-token, saved on
# first start, or FACTORY_TOKEN). The factory logs an "Open:" link carrying it
# and opens that link in your browser itself.
#
# Stop with Ctrl-C.
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")

if (-not (Get-Command go -ErrorAction SilentlyContinue)) { Write-Error "Go is required."; exit 1 }

$port = if ($env:PORT) { $env:PORT } else { "8799" }
New-Item -ItemType Directory -Force -Path models, images, config | Out-Null

. (Join-Path $PSScriptRoot 'version.ps1')   # build identity shown in the UI sidebar
Write-Host "Building factory (native) v$FactoryVersion ($FactoryCommit)..."
go build -ldflags $FactoryLdflags -o local-llm.exe ./cmd/builder   # UI is embedded from internal/server/web (committed)

if (Get-Command podman -ErrorAction SilentlyContinue) {
  Write-Host "podman: $(podman --version)"
} else {
  Write-Warning "podman not on PATH — install it if the Containers tab is empty (docker still works)."
}

# The FileFreak archive (docs/FILEFREAK.md) runs tools/filefreak with Node.js 20
# or later. Without it the factory still works: model files come from Hugging Face.
$nodeOK = $false
if (Get-Command node -ErrorAction SilentlyContinue) {
  & node -e "process.exit(+process.versions.node.split('.')[0] >= 20 ? 0 : 1)"
  $nodeOK = ($LASTEXITCODE -eq 0)
}
if ($nodeOK) {
  $ffLock = "tools/filefreak/node_modules/.package-lock.json"
  if (-not (Test-Path $ffLock) -or ((Get-Item "tools/filefreak/package-lock.json").LastWriteTime -gt (Get-Item $ffLock).LastWriteTime)) {
    Write-Host "Installing the FileFreak archive helper (tools/filefreak)..."
    Push-Location tools/filefreak
    npm ci --omit=dev --no-audit --no-fund --silent
    if ($LASTEXITCODE -ne 0) { Write-Warning "npm ci in tools/filefreak failed, so the FileFreak archive is off: model files download from Hugging Face." }
    Pop-Location
  }
} else {
  Write-Warning "Node.js 20 or later isn't on PATH, so the FileFreak archive is off: model files download from Hugging Face. Install it (winget install OpenJS.NodeJS.LTS) to use the archive."
}

Write-Host "Factory (native — sees host podman + docker): http://127.0.0.1:$port   (Ctrl-C to stop)"
Write-Host "Its access link (Open: http://127.0.0.1:$port/?token=...) is in the log below."
# The factory opens the browser itself, at the link with the token (a plain
# http://127.0.0.1:$port tab would only show the lock screen).
$openFlag = if ($env:OPEN -eq "0") { "-open=false" } else { "-open=true" }

& .\local-llm.exe -addr "127.0.0.1:$port" $openFlag
