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
# The factory's API needs its access token (config/factory-token, saved on
# first start, or FACTORY_TOKEN). The factory logs an "Open:" link carrying it
# and opens that link in your browser itself.
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

. scripts/version.sh   # build identity shown in the UI sidebar
echo "Building factory (native) v$FACTORY_VERSION ($FACTORY_COMMIT)..."
go build -ldflags "$FACTORY_LDFLAGS" -o local-llm ./cmd/builder   # UI is embedded from internal/server/web (committed)

if command -v podman >/dev/null 2>&1; then
  echo "podman: $(podman --version 2>/dev/null || echo installed)"
else
  echo "WARN: podman not on PATH — the factory falls back to /opt/podman/bin; install Podman Desktop if the Containers tab is still empty."
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
  echo "WARN: Node.js 20 or later isn't on PATH, so the FileFreak archive is off: model files download from Hugging Face. Install it (brew install node) to use the archive."
fi

echo "Factory (native — sees host podman + docker): http://127.0.0.1:$PORT   (Ctrl-C to stop)"
echo "Its access link (Open: http://127.0.0.1:$PORT/?token=...) is in the log below."
# The factory opens the browser itself, at the link with the token (a plain
# http://127.0.0.1:$PORT tab would only show the lock screen).
OPEN_FLAG=-open=false
if [ "${OPEN:-1}" = 1 ] && command -v open >/dev/null 2>&1; then OPEN_FLAG=-open=true; fi

exec ./local-llm -addr "127.0.0.1:$PORT" "$OPEN_FLAG"
