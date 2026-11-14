#!/usr/bin/env bash
#
# Build the orb binary, with its version stamped in.
#
# Usage: scripts/build.sh [options] [-- extra go build args...]
#
# Options:
#   --install          Also `go install` the binary into $(go env GOBIN), or
#                      $(go env GOPATH)/bin.
#   -o, --output PATH  Output path. Defaults to bin/orb (bin/orb.exe on Windows).
#
# VERSION, COMMIT and DATE override what is stamped; otherwise they come from git.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXE="$(go env GOEXE)"
OUTPUT="${OUTPUT:-${ROOT_DIR}/bin/orb${EXE}}"
GO_INSTALL=false
GO_BUILD_ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install)
      GO_INSTALL=true
      shift
      ;;
    -o | --output)
      if [[ $# -lt 2 ]]; then
        echo "error: $1 requires a path" >&2
        exit 2
      fi
      OUTPUT="$2"
      shift 2
      ;;
    --)
      shift
      GO_BUILD_ARGS+=("$@")
      break
      ;;
    -h | --help)
      sed -n '2,12p' "${BASH_SOURCE[0]}" >&2
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      exit 2
      ;;
  esac
done

cd "${ROOT_DIR}"

# Build metadata stamped into the binary, so `orb version` can say which commit
# it was built from. A dirty tree is marked as such, because a build from
# uncommitted changes is not reproducible from its commit alone.
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

LDFLAGS="-s -w"
LDFLAGS+=" -X main.version=${VERSION}"
LDFLAGS+=" -X main.commit=${COMMIT}"
LDFLAGS+=" -X main.date=${DATE}"

echo "==> building ${OUTPUT} (version=${VERSION} commit=${COMMIT})"
mkdir -p "$(dirname "${OUTPUT}")"
go build -trimpath -ldflags "${LDFLAGS}" -o "${OUTPUT}" \
  ${GO_BUILD_ARGS[@]+"${GO_BUILD_ARGS[@]}"} ./cmd/orb
echo "==> built ${OUTPUT}"

if [[ "${GO_INSTALL}" == "true" ]]; then
  go install -trimpath -ldflags "${LDFLAGS}" ./cmd/orb
  echo "==> installed $(go env GOBIN | grep . || echo "$(go env GOPATH)/bin")/orb${EXE}"
fi
