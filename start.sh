#!/usr/bin/env bash
# Start the local-llm FACTORY (runs the factory itself in Docker — cross-platform,
# no Go toolchain needed on the host).
#
# The factory builds/runs model images via the host's Docker daemon
# (Docker-out-of-Docker), so the host docker socket is mounted. Weights,
# tarballs, and the catalog are bind-mounted back to ./models ./images ./config.
#
#   ./start.sh            # build + run, UI at http://localhost:8799
#   PORT=9000 ./start.sh
#
# (For native dev without Docker you can still run:  go run ./cmd/builder)
set -euo pipefail
cd "$(dirname "$0")"

command -v docker >/dev/null 2>&1 || { echo "Docker is required and must be running." >&2; exit 1; }

# Podman Desktop on macOS installs the CLI under /opt/podman/bin, which isn't on
# a non-login shell's PATH. Add it so the Podman-socket detection below works.
[ -x /opt/podman/bin/podman ] && PATH="/opt/podman/bin:$PATH"

NAME=local-llm-factory
PORT="${PORT:-8799}"
mkdir -p models images config

echo "Building factory image ($NAME)..."
docker build -t "$NAME" -f Dockerfile.factory .

docker rm -f "$NAME" >/dev/null 2>&1 || true

# Optional Podman engine: if a host Podman machine (macOS/Windows libkrun/QEMU) or
# a native rootful Podman socket (Linux) is present, mount it so the factory's
# "Podman" engine option works — this is the macOS GPU (Vulkan/Metal) path. No-op
# when Podman isn't installed, so Docker-only users are unaffected.
PODMAN_ARGS=()
if command -v podman >/dev/null 2>&1; then
    PSOCK="$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}' 2>/dev/null | head -n1)"
    if [ -z "${PSOCK:-}" ] && [ -S /run/podman/podman.sock ]; then PSOCK=/run/podman/podman.sock; fi
    if [ -n "${PSOCK:-}" ] && [ -S "$PSOCK" ]; then
        PODMAN_ARGS=(-v "$PSOCK:/run/podman/podman.sock" -e "CONTAINER_HOST=unix:///run/podman/podman.sock")
        echo "Podman machine detected — enabling the Podman engine (socket: $PSOCK)"
    fi
fi

echo "Starting factory container..."
docker run -d --name "$NAME" \
    -p "$PORT:8799" \
    -e "HOST_DIR=$PWD" \
    -v //var/run/docker.sock:/var/run/docker.sock \
    -v "$PWD/models:/app/models" \
    -v "$PWD/images:/app/images" \
    -v "$PWD/config:/app/config" \
    --add-host host.docker.internal:host-gateway \
    ${PODMAN_ARGS[@]+"${PODMAN_ARGS[@]}"} \
    "$NAME"

echo "local-llm factory is running at http://localhost:$PORT"
echo "Logs:  docker logs -f $NAME"
echo "Stop:  ./stop.sh"
