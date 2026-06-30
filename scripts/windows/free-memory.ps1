# Reclaim memory the Docker/WSL2 VM is holding as page cache, WITHOUT stopping
# any containers. It drops the VM's Linux page cache via a privileged throwaway
# container (which shares the VM kernel). WSL2 then hands the freed memory back
# to Windows (gradually by default).
#
# For a FULL, instant release: `wsl --shutdown` (stops ALL containers), or enable
# auto-reclaim once in %UserProfile%\.wslconfig:
#     [experimental]
#     autoMemoryReclaim=gradual

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "ERROR: Docker is required and must be running." -ForegroundColor Red
    exit 1
}

Write-Host "Reclaiming Docker/WSL2 memory (containers keep running)..." -ForegroundColor Cyan
docker run --rm --privileged alpine sh -c "echo '--- before ---'; free -h | grep -i mem; sync; echo 3 > /proc/sys/vm/drop_caches; echo '--- after ---'; free -h | grep -i mem"
if ($LASTEXITCODE -ne 0) { Write-Host "ERROR: reclaim failed." -ForegroundColor Red; exit 1 }
Write-Host "Page cache dropped. WSL2 returns it to Windows gradually." -ForegroundColor Green
