# Sourced by the start scripts to stamp the factory build with its version, so
# the UI (sidebar) and /api/version show exactly which code is running.
#   FACTORY_VERSION  VERSION file's major.minor + commit count (every commit to
#                    main bumps it), e.g. 1.0.153
#   FACTORY_COMMIT   short SHA, with "-dirty" when tracked files have local edits
#   FACTORY_LDFLAGS  the matching `go build -ldflags` value
# Repo root: from this file's path under bash; otherwise the caller's directory
# (every start script cd's to the repo root before sourcing this).
_vroot="$PWD"
if [ -n "${BASH_SOURCE[0]:-}" ]; then _vroot="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; fi
_vbase=""
if [ -f "$_vroot/VERSION" ]; then _vbase="$(tr -d '[:space:]' < "$_vroot/VERSION")"; fi
_vcount="$(git -C "$_vroot" rev-list --count HEAD 2>/dev/null || echo 0)"
FACTORY_VERSION="${_vbase:-0.0}.${_vcount}"
FACTORY_COMMIT="$(git -C "$_vroot" rev-parse --short HEAD 2>/dev/null || echo unknown)"
if [ -n "$(git -C "$_vroot" status --porcelain --untracked-files=no 2>/dev/null)" ]; then
  FACTORY_COMMIT="${FACTORY_COMMIT}-dirty"
fi
_vpkg=github.com/seanbrill/local-llm/internal/version
FACTORY_LDFLAGS="-X ${_vpkg}.Version=${FACTORY_VERSION} -X ${_vpkg}.Commit=${FACTORY_COMMIT}"
unset _vroot _vbase _vcount _vpkg
