#!/usr/bin/env bash
# Start the local-llm FACTORY as a NATIVE host process (not in Docker).
#
# Why this exists: on macOS the GPU model containers run under Podman, which lives
# in its own libkrun VM. The Docker-based start.sh runs the factory inside Docker's
# OWN VM, and one VM can't reach the other's container socket — so over there the
# Containers tab comes up empty. Run the factory natively and it shells out to the
# host's podman/docker directly, so it sees and manages those model containers
# (Start / Stop / logs all work). This mirrors how protoTree talks to the engines.
#
#   ./start-native.sh                 # http://127.0.0.1:8799
#   PORT=9000 ./start-native.sh
#   OPEN=0 ./start-native.sh          # don't auto-open the browser
#
# Stop with Ctrl-C.
set -euo pipefail
cd "$(dirname "$0")/../.."            # -> llm-factory repo root

command -v go >/dev/null 2>&1 || { echo "Go is required (brew install go)." >&2; exit 1; }

# Podman Desktop installs its CLI under /opt/podman/bin, which a non-login/service
# PATH omits. The factory also falls back to this dir internally, but adding it
# here lets the host podman CLI resolve its own connection config cleanly too.
[ -d /opt/podman/bin ] && PATH="/opt/podman/bin:$PATH"

PORT="${PORT:-8799}"
mkdir -p models images config

echo "Building factory (native)..."
go build -o local-llm ./cmd/builder   # UI is embedded from internal/server/web (committed)

if command -v podman >/dev/null 2>&1; then
  echo "podman: $(podman --version 2>/dev/null || echo installed)"
else
  echo "WARN: podman not on PATH — the factory falls back to /opt/podman/bin; install Podman Desktop if the Containers tab is still empty."
fi

echo "Factory (native — sees host podman + docker): http://127.0.0.1:$PORT   (Ctrl-C to stop)"
if [ "${OPEN:-1}" = 1 ] && command -v open >/dev/null 2>&1; then ( sleep 1; open "http://127.0.0.1:$PORT" ) & fi

exec ./local-llm -addr "127.0.0.1:$PORT"
