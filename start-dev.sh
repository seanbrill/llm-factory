#!/usr/bin/env bash
# Start the factory in DEV mode with independent hot-reload:
#   • Backend: wgo rebuilds/restarts the Go server when *.go files change.
#   • UI:      served from ./internal/server/web on disk — edit + refresh, no rebuild.
#
# The whole repo is bind-mounted into the container, so host edits are live.
#
#   ./start-dev.sh            # UI at http://localhost:8799, hot-reloads
#   PORT=9000 ./start-dev.sh
#
# Stop with ./stop.sh (removes prod and dev containers).
set -euo pipefail
cd "$(dirname "$0")"

command -v docker >/dev/null 2>&1 || { echo "Docker is required and must be running." >&2; exit 1; }

NAME=local-llm-factory-dev
PORT="${PORT:-8799}"
mkdir -p models images config

echo "Building dev image ($NAME)..."
docker build -t "$NAME" -f Dockerfile.dev .

docker rm -f "$NAME" >/dev/null 2>&1 || true

# Optional Podman engine (parity with start.sh): mount a host Podman machine /
# rootful socket when present so the Podman engine option works in dev too.
PODMAN_ARGS=()
if command -v podman >/dev/null 2>&1; then
    PSOCK="$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}' 2>/dev/null | head -n1)"
    if [ -z "${PSOCK:-}" ] && [ -S /run/podman/podman.sock ]; then PSOCK=/run/podman/podman.sock; fi
    if [ -n "${PSOCK:-}" ] && [ -S "$PSOCK" ]; then
        PODMAN_ARGS=(-v "$PSOCK:/run/podman/podman.sock" -e "CONTAINER_HOST=unix:///run/podman/podman.sock")
        echo "Podman machine detected — enabling the Podman engine (socket: $PSOCK)"
    fi
fi

echo "Starting dev container (hot-reload)..."
docker run -d --name "$NAME" \
    -p "$PORT:8799" \
    -e "HOST_DIR=$PWD" \
    -v //var/run/docker.sock:/var/run/docker.sock \
    -v "$PWD:/app" \
    -v local-llm-gocache:/root/.cache/go-build \
    --add-host host.docker.internal:host-gateway \
    "${PODMAN_ARGS[@]}" \
    "$NAME"

echo "Dev factory running at http://localhost:$PORT (hot-reload on)"
echo "  UI:      edit internal/server/web/* -> refresh the browser"
echo "  Backend: edit *.go -> auto rebuild/restart (watch: docker logs -f $NAME)"
echo "Stop:  ./stop.sh"
