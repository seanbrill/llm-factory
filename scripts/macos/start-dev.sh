#!/usr/bin/env bash
# Start the factory in DEV mode with independent hot-reload:
#   • Backend: wgo rebuilds/restarts the Go server when *.go files change.
#   • UI:      served from ./internal/server/web on disk — edit + refresh, no rebuild.
#
# The whole repo is bind-mounted into the container, so host edits are live.
#
#   ./start-dev.sh            # UI at http://localhost:8799, hot-reloads
#   PORT=9000 ./start-dev.sh
#   BIND=0.0.0.0 ./start-dev.sh   # also reachable from your network (default:
#                                 # this computer only)
#
# The factory's API needs its access token (config/factory-token, saved on
# first start); the "Open:" link printed at the end carries it.
#
# Stop with ./stop.sh (removes prod and dev containers).
set -euo pipefail
cd "$(dirname "$0")/../.."

command -v docker >/dev/null 2>&1 || { echo "Docker is required and must be running." >&2; exit 1; }

NAME=local-llm-factory-dev
PORT="${PORT:-8799}"
BIND="${BIND:-127.0.0.1}"
mkdir -p models images config workspace

echo "Building UI (Svelte -> internal/server/web)..."
docker run --rm -v "$PWD:/app" -w /app/ui node:20-alpine sh -c "npm install --no-audit --no-fund --silent && npm run build" || { echo "ERROR: UI build failed."; exit 1; }

# UI hot-reload: a background `vite build --watch` rebuilds internal/server/web on
# every ui/src edit; the dev container serves it from disk, so just refresh the
# browser (the Go backend hot-reloads separately via wgo). CHOKIDAR_USEPOLLING is
# required so edits are seen across Docker Desktop bind mounts (same reason wgo
# uses -poll). Named so stop.sh removes it; reuses node_modules from the build above.
docker rm -f local-llm-ui-watch >/dev/null 2>&1 || true
docker run -d --name local-llm-ui-watch -e CHOKIDAR_USEPOLLING=true -e VITE_WATCH=1 \
    -v "$PWD:/app" -w /app/ui node:20-alpine \
    sh -c "npm run build -- --watch" >/dev/null
echo "UI watch on: edit ui/src/* -> auto-rebuild -> refresh the browser."

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
# HOST_HOME / FACTORY_PORT serve the agent harness (Harness page); the repo
# mount already provides /app/skills and /app/workspace.
docker run -d --name "$NAME" \
    -p "$BIND:$PORT:8799" \
    -e "HOST_DIR=$PWD" \
    -e "HOST_HOME=$HOME" \
    -e "FACTORY_PORT=$PORT" \
    -e "FACTORY_HOST_MEM_BYTES=$(sysctl -n hw.memsize 2>/dev/null || true)" \
    -v //var/run/docker.sock:/var/run/docker.sock \
    -v "$PWD:/app" \
    -v local-llm-gocache:/root/.cache/go-build \
    --add-host host.docker.internal:host-gateway \
    ${PODMAN_ARGS[@]+"${PODMAN_ARGS[@]}"} \
    "$NAME"

echo "Dev factory running at http://localhost:$PORT (hot-reload on)"
# The access link: wait (up to ~20 s on a first start) for the factory to save
# its token, then print the UI's address with it. The UI is served on $PORT
# too (vite build --watch writes it to disk), so that's the port to open.
TOKEN=""
tries=0
while [ "$tries" -lt 40 ]; do
    if [ -r config/factory-token ]; then TOKEN="$(tr -d '[:space:]' < config/factory-token || true)"; fi
    [ "${#TOKEN}" -ge 24 ] && break
    TOKEN=""
    tries=$((tries + 1))
    sleep 0.5
done
if [ -n "$TOKEN" ]; then
    # Percent-encode it for the link: a generated token is hex, but one you
    # wrote into the file yourself may hold characters like + & # %.
    LINK_TOKEN=""
    pos=0
    while [ "$pos" -lt "${#TOKEN}" ]; do
        ch="${TOKEN:$pos:1}"
        case "$ch" in
            [[:alnum:]._~-]) LINK_TOKEN="$LINK_TOKEN$ch" ;;
            *) LINK_TOKEN="$LINK_TOKEN$(printf '%%%02X' "'$ch")" ;;
        esac
        pos=$((pos + 1))
    done
    echo "Open: http://localhost:$PORT/?token=$LINK_TOKEN"
else
    echo "Open: the access link is in the factory log (docker logs $NAME); config/factory-token didn't appear yet (the first dev start compiles the factory)."
fi
[ "$BIND" = "127.0.0.1" ] || echo "WARNING: listening on $BIND:$PORT — other machines can reach the factory. They still need its access token, but plain HTTP sends it unencrypted: do this only on a network you trust."
echo "  UI:      edit ui/src/* -> auto-rebuild (vite watch) -> refresh the browser"
echo "  Backend: edit *.go -> auto rebuild/restart (watch: docker logs -f $NAME)"
echo "Stop:  ./stop.sh"
