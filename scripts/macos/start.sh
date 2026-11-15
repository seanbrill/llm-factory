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
#   BIND=0.0.0.0 ./start.sh   # also reachable from your network (default: this
#                             # computer only)
#   FACTORY_DOCKER_SOCK=~/.lima/docker/sock/docker.sock ./start.sh
#                             # drive another Docker Engine (default: the
#                             # host's /var/run/docker.sock)
#
# The factory's API needs its access token, which the factory saves in
# config/factory-token on first start. The "Open:" link printed at the end
# carries it and signs your browser in.
#
# (For native dev without Docker you can still run:  go run ./cmd/builder)
set -euo pipefail
cd "$(dirname "$0")/../.."

command -v docker >/dev/null 2>&1 || { echo "Docker is required and must be running." >&2; exit 1; }

# Podman Desktop on macOS installs the CLI under /opt/podman/bin, which isn't on
# a non-login shell's PATH. Add it so the Podman-socket detection below works.
[ -x /opt/podman/bin/podman ] && PATH="/opt/podman/bin:$PATH"

NAME=local-llm-factory
PORT="${PORT:-8799}"
BIND="${BIND:-127.0.0.1}"
mkdir -p models images config media workspace packages

# Stamp the build with its version (shown in the UI sidebar). Computed before
# the UI build so "-dirty" reflects only your own local edits.
. scripts/version.sh
echo "Building UI (Svelte -> internal/server/web)..."
docker run --rm -v "$PWD:/app" -w /app/ui node:20-alpine sh -c "npm install --no-audit --no-fund --silent && npm run build" || { echo "ERROR: UI build failed."; exit 1; }

echo "Building factory image ($NAME) v$FACTORY_VERSION ($FACTORY_COMMIT)..."
docker build --build-arg "FACTORY_VERSION=$FACTORY_VERSION" --build-arg "FACTORY_COMMIT=$FACTORY_COMMIT" \
  -t "$NAME" -f Dockerfile.factory .

docker rm -f "$NAME" >/dev/null 2>&1 || true

# Optional Podman engine: if a host Podman machine (macOS/Windows libkrun/QEMU) or
# a native rootful Podman socket (Linux) is present, mount it so the factory's
# "Podman" engine option works — this is the macOS GPU (Vulkan/Metal) path. No-op
# when Podman isn't installed, so Docker-only users are unaffected.
PODMAN_ARGS=()
if command -v podman >/dev/null 2>&1; then
    # A Podman machine whose shared folders stopped answering (it can happen
    # after the Mac sleeps) can't mount anything from this Mac: every run then
    # fails with "statfs ...: connection refused". A restart brings them back.
    # (A machine that isn't running answers differently and is left alone.)
    SHARE_CHECK="$(podman machine ssh "ls -d '$PWD'" 2>&1 || true)"
    case "$SHARE_CHECK" in *[Cc]onnection\ refused*)
        echo "The Podman machine can't see this Mac's folders (its shared folders stopped answering). Restarting it..."
        { podman machine stop && podman machine start; } ||
            echo "WARNING: couldn't restart the Podman machine. Run: podman machine stop && podman machine start" >&2
        ;;
    esac
    PSOCK="$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}' 2>/dev/null | head -n1)"
    if [ -z "${PSOCK:-}" ] && [ -S /run/podman/podman.sock ]; then PSOCK=/run/podman/podman.sock; fi
    if [ -n "${PSOCK:-}" ] && [ -S "$PSOCK" ]; then
        PODMAN_ARGS=(-v "$PSOCK:/run/podman/podman.sock" -e "CONTAINER_HOST=unix:///run/podman/podman.sock")
        # GPU hint for the UI's "fits your system" badges: a libkrun/krunkit machine
        # paravirtualizes the Apple/host GPU into a Vulkan device. (machine inspect
        # is a host command, so we detect it here and pass it into the container.)
        # The machine's config dir path encodes the provider (…/machine/libkrun).
        # `|| true` keeps a detection hiccup from aborting startup under `set -e`.
        MACHINE_CFG="$(podman machine inspect --format '{{.ConfigDir.Path}}' 2>/dev/null | head -n1 || true)"
        case "$MACHINE_CFG" in *krun*) PODMAN_ARGS+=(-e "FACTORY_GPU=vulkan") ;; esac
        echo "Podman machine detected — enabling the Podman engine (socket: $PSOCK)"
    fi
fi

# The Docker engine the factory builds and runs models on: the host's default
# socket, or another Docker Engine's when FACTORY_DOCKER_SOCK names its socket
# on this computer — for example Docker Engine in a Lima or Colima VM started
# with vmType krunkit, whose virtio-gpu can give containers the Mac's GPU
# (experimental). It's mounted where the default one goes; the factory
# container itself still runs on the default Docker.
DOCKER_SOCK_ARGS=(-v //var/run/docker.sock:/var/run/docker.sock)
if [ -n "${FACTORY_DOCKER_SOCK:-}" ]; then
    if [ ! -S "$FACTORY_DOCKER_SOCK" ]; then
        echo "FACTORY_DOCKER_SOCK=$FACTORY_DOCKER_SOCK isn't a socket. Start that VM, or unset FACTORY_DOCKER_SOCK to use the default Docker." >&2
        exit 1
    fi
    DOCKER_SOCK_ARGS=(-v "$FACTORY_DOCKER_SOCK:/var/run/docker.sock")
    echo "Docker engine for models: $FACTORY_DOCKER_SOCK (FACTORY_DOCKER_SOCK)"
fi

echo "Starting factory container..."
# HOST_HOME / FACTORY_PORT and the skills + workspace mounts serve the agent
# harness (Harness page): its workspace terminal mounts host paths, and Open
# WebUI reaches the model gateway on the published port.
docker run -d --name "$NAME" \
    -p "$BIND:$PORT:8799" \
    -e "HOST_DIR=$PWD" \
    -e "HOST_HOME=$HOME" \
    -e "FACTORY_PORT=$PORT" \
    -e FILEFREAK_ARCHIVE_URL \
    -e "FACTORY_HOST_MEM_BYTES=$(sysctl -n hw.memsize 2>/dev/null || true)" \
    -e "FACTORY_HOST_OS=$(uname -s 2>/dev/null || true)" \
    "${DOCKER_SOCK_ARGS[@]}" \
    -v "$PWD/models:/app/models" \
    -v "$PWD/images:/app/images" \
    -v "$PWD/config:/app/config" \
    -v "$PWD/media:/app/media" \
    -v "$PWD/packages:/app/packages" \
    -v "$PWD/skills:/app/skills:ro" \
    -v "$PWD/workspace:/app/workspace" \
    --add-host host.docker.internal:host-gateway \
    ${PODMAN_ARGS[@]+"${PODMAN_ARGS[@]}"} \
    "$NAME"

echo "local-llm factory is running at http://localhost:$PORT"
# The access link: wait (up to ~20 s on a first start) for the factory to save
# its token, then print the UI's address with it.
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
    echo "Open: the access link is in the factory log (docker logs $NAME); config/factory-token didn't appear."
fi
[ "$BIND" = "127.0.0.1" ] || echo "WARNING: listening on $BIND:$PORT — other machines can reach the factory. They still need its access token, but plain HTTP sends it unencrypted: do this only on a network you trust."
echo "Logs:  docker logs -f $NAME"
echo "Stop:  ./stop.sh"
