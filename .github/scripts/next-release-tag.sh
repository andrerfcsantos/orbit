#!/usr/bin/env bash
set -euo pipefail

marker_source="${1:-}"
if [[ -z "${marker_source}" ]]; then
  marker_source="$(git log -1 --pretty=%B)"
fi

latest_tag="$(
  git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname |
    grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' |
    head -n 1 || true
)"

if [[ -z "${latest_tag}" ]]; then
  echo "v0.1.0"
  exit 0
fi

version="${latest_tag#v}"
IFS=. read -r major minor patch <<< "${version}"

bump="minor"
if grep -Eiq '(^|[^[:alnum:]])release:[[:space:]]*major([^[:alnum:]]|$)' <<< "${marker_source}"; then
  bump="major"
elif grep -Eiq '(^|[^[:alnum:]])release:[[:space:]]*patch([^[:alnum:]]|$)' <<< "${marker_source}"; then
  bump="patch"
elif grep -Eiq '(^|[^[:alnum:]])release:[[:space:]]*minor([^[:alnum:]]|$)' <<< "${marker_source}"; then
  bump="minor"
fi

case "${bump}" in
  major)
    major=$((major + 1))
    minor=0
    patch=0
    ;;
  minor)
    minor=$((minor + 1))
    patch=0
    ;;
  patch)
    patch=$((patch + 1))
    ;;
esac

echo "v${major}.${minor}.${patch}"
