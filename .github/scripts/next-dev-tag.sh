#!/usr/bin/env bash
set -euo pipefail

bump="${1:-minor}"
ref_name="${2:-$(git rev-parse --abbrev-ref HEAD)}"
run_number="${3:-0}"

case "${bump}" in
  major | minor | patch) ;;
  *)
    echo "invalid release bump ${bump}; expected major, minor, or patch" >&2
    exit 2
    ;;
esac

base_tag="$("$(dirname "${BASH_SOURCE[0]}")/next-release-tag.sh" "release: ${bump}")"
base_version="${base_tag#v}"
slug="$(
  printf '%s' "${ref_name}" |
    tr '[:upper:]' '[:lower:]' |
    sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//' |
    cut -c 1-40 |
    sed -E 's/-+$//'
)"

if [[ -z "${slug}" ]]; then
  slug="dev"
fi

echo "v${base_version}-dev.${slug}.${run_number}"
