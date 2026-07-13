#!/usr/bin/env bash
# Start the local-llm FACTORY as a NATIVE host process (not in Docker).
#
# Running natively lets the factory shell out to the host's podman/docker CLIs
# directly, so the Containers tab sees and manages model containers even when the
# containerized factory can't reach the engine socket (the common macOS Podman
# case; also handy on Linux for a quick socketless run). Mirrors how protoTree
# talks to the engines.
#
#   ./start-native.sh                 # http://127.0.0.1:8799
#   PORT=9000 ./start-native.sh
#   OPEN=0 ./start-native.sh          # don't auto-open the browser
#
# Stop with Ctrl-C.
set -euo pipefail
cd "$(dirname "$0")/../.."            # -> llm-factory repo root

command -v go >/dev/null 2>&1 || { echo "Go is required." >&2; exit 1; }

# Common CLI install dirs a service PATH may omit (Podman Desktop, Homebrew).
for d in /opt/podman/bin /usr/local/bin /opt/homebrew/bin; do
  [ -d "$d" ] && case ":$PATH:" in *":$d:"*) ;; *) PATH="$d:$PATH";; esac
done

PORT="${PORT:-8799}"
mkdir -p models images config

echo "Building factory (native)..."
go build -o local-llm ./cmd/builder   # UI is embedded from internal/server/web (committed)

if command -v podman >/dev/null 2>&1; then
  echo "podman: $(podman --version 2>/dev/null || echo installed)"
else
  echo "WARN: podman not on PATH — install it if the Containers tab is empty (docker still works)."
fi

echo "Factory (native — sees host podman + docker): http://127.0.0.1:$PORT   (Ctrl-C to stop)"
if [ "${OPEN:-1}" = 1 ] && command -v xdg-open >/dev/null 2>&1; then ( sleep 1; xdg-open "http://127.0.0.1:$PORT" ) & fi

exec ./local-llm -addr "127.0.0.1:$PORT"
