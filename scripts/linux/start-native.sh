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
#   FACTORY_DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock ./start-native.sh
#                                     # drive another Docker Engine
#
# The factory's API needs its access token (config/factory-token, saved on
# first start, or FACTORY_TOKEN). The factory logs an "Open:" link carrying it
# and opens that link in your browser itself.
#
# Stop with Ctrl-C.
set -euo pipefail
cd "$(dirname "$0")/../.."            # -> llm-factory repo root

command -v go >/dev/null 2>&1 || { echo "Go is required." >&2; exit 1; }

# Common CLI install dirs a service PATH may omit (Podman Desktop, Homebrew).
for d in /opt/podman/bin /usr/local/bin /opt/homebrew/bin; do
  [ -d "$d" ] && case ":$PATH:" in *":$d:"*) ;; *) PATH="$d:$PATH";; esac
done

# Another Docker Engine to build and run models on, by its socket — for example
# rootless Docker's. The docker CLI the factory runs follows DOCKER_HOST.
if [ -n "${FACTORY_DOCKER_SOCK:-}" ]; then
  if [ ! -S "$FACTORY_DOCKER_SOCK" ]; then
    echo "FACTORY_DOCKER_SOCK=$FACTORY_DOCKER_SOCK isn't a socket. Start that engine, or unset FACTORY_DOCKER_SOCK to use the default Docker." >&2
    exit 1
  fi
  export DOCKER_HOST="unix://$FACTORY_DOCKER_SOCK"
  echo "Docker engine for models: $FACTORY_DOCKER_SOCK (FACTORY_DOCKER_SOCK)"
fi

PORT="${PORT:-8799}"
mkdir -p models images config

. scripts/linux/version.sh   # build identity shown in the UI sidebar
echo "Building factory (native) v$FACTORY_VERSION ($FACTORY_COMMIT)..."
go build -ldflags "$FACTORY_LDFLAGS" -o local-llm ./cmd/builder   # UI is embedded from internal/server/web (committed)

if command -v podman >/dev/null 2>&1; then
  echo "podman: $(podman --version 2>/dev/null || echo installed)"
else
  echo "WARN: podman not on PATH — install it if the Containers tab is empty (docker still works)."
fi

# The FileFreak archive (docs/FILEFREAK.md) runs tools/filefreak with Node.js 20
# or later. Without it the factory still works: model files come from Hugging Face.
if command -v node >/dev/null 2>&1 && node -e 'process.exit(+process.versions.node.split(".")[0] >= 20 ? 0 : 1)'; then
  if [ ! -f tools/filefreak/node_modules/.package-lock.json ] || [ tools/filefreak/package-lock.json -nt tools/filefreak/node_modules/.package-lock.json ]; then
    echo "Installing the FileFreak archive helper (tools/filefreak)..."
    (cd tools/filefreak && npm ci --omit=dev --no-audit --no-fund --silent) \
      || echo "WARN: npm ci in tools/filefreak failed, so the FileFreak archive is off: model files download from Hugging Face."
  fi
else
  echo "WARN: Node.js 20 or later isn't on PATH, so the FileFreak archive is off: model files download from Hugging Face. Install it (nodejs.org, or your package manager) to use the archive."
fi

echo "Factory (native — sees host podman + docker): http://127.0.0.1:$PORT   (Ctrl-C to stop)"
echo "Its access link (Open: http://127.0.0.1:$PORT/?token=...) is in the log below."
# The factory opens the browser itself, at the link with the token (a plain
# http://127.0.0.1:$PORT tab would only show the lock screen).
OPEN_FLAG=-open=false
if [ "${OPEN:-1}" = 1 ] && command -v xdg-open >/dev/null 2>&1; then OPEN_FLAG=-open=true; fi

exec ./local-llm -addr "127.0.0.1:$PORT" "$OPEN_FLAG"
