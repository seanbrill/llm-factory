#!/usr/bin/env bash
# Reclaim memory the Docker VM is holding as page cache, WITHOUT stopping any
# containers — it drops the VM's Linux page cache via a privileged throwaway
# container (which shares the VM kernel). On Docker Desktop (macOS) this frees
# the LinuxKit VM; on native Linux it frees the host page cache directly.
set -euo pipefail
command -v docker >/dev/null 2>&1 || { echo "Docker is required and must be running." >&2; exit 1; }
echo "Reclaiming Docker memory (containers keep running)..."
docker run --rm --privileged alpine sh -c "echo '--- before ---'; free -h | grep -i mem; sync; echo 3 > /proc/sys/vm/drop_caches; echo '--- after ---'; free -h | grep -i mem"
echo "Done — freed page cache returned to the host."
