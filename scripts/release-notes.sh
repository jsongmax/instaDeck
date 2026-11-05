#!/usr/bin/env bash
# Prints the CHANGELOG.md section of a version, and fails if it is missing or empty.
# Usage: scripts/release-notes.sh v1.2.3
set -euo pipefail

tag="${1:-}"
if [[ -z "$tag" ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH[-prerelease]" >&2
  exit 2
fi
version="${tag#v}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Exact prefix matching, because dots in a version would be wildcards in a regex.
notes="$(tr -d '\r' < "$root/CHANGELOG.md" | awk -v heading="## [$version]" '
  index($0, "## ") == 1 { active = (index($0, heading) == 1); next }
  active { print }
')"

if [[ -z "${notes//[[:space:]]/}" ]]; then
  echo "CHANGELOG.md has no notes for $version: add a \"## [$version]\" section" >&2
  exit 1
fi

printf '%s\n' "$notes"
