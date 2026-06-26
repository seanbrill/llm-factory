# Stop and remove the local-llm factory container.
# Note: model containers the factory launched run on the host daemon
# independently and are NOT stopped here — remove those from the UI (or
# `docker rm -f <name>`).

Set-Location -Path (Join-Path $PSScriptRoot '..\..')

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: Docker not found." -ForegroundColor Red
    exit 1
}

# Remove both the prod and dev factory containers (existence-checked so a
# missing container doesn't emit a noisy error).
$any = $false
foreach ($name in @("local-llm-factory", "local-llm-factory-dev")) {
    if (docker ps -aq -f "name=^$name$") {
        docker rm -f $name | Out-Null
        Write-Host "removed $name" -ForegroundColor Green
        $any = $true
    }
}
if (-not $any) { Write-Host "no factory containers running." -ForegroundColor Yellow }
