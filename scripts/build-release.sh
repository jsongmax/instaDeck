#!/usr/bin/env bash
# Builds the release archives and their checksums into dist/.
# Usage: scripts/build-release.sh v1.2.3
set -euo pipefail

tag="${1:-}"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH[-prerelease]" >&2
  exit 2
fi
version="${tag#v}"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

# Windows ships a python3 stub that exists but does not run, so test by running.
python=""
for candidate in python3 python; do
  if "$candidate" -c "" >/dev/null 2>&1; then
    python="$candidate"
    break
  fi
done
if [[ -z "$python" ]]; then
  echo "python is required to create zip archives" >&2
  exit 1
fi

rm -rf dist
mkdir -p dist/.stage

(cd web && npm ci && npm run build)

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  goos="${target%/*}"
  goarch="${target#*/}"
  name="instadeck_${version}_${goos}_${goarch}"
  stage="dist/.stage/$name"
  binary="instadeck"
  if [[ "$goos" == "windows" ]]; then
    binary="instadeck.exe"
  fi

  mkdir -p "$stage"
  echo "building $name"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
    -ldflags="-s -w -X main.version=$version" -o "$stage/$binary" ./cmd/instadeck
  cp README.md LICENSE "$stage/"
  cp -R deploy "$stage/"

  if [[ "$goos" == "windows" ]]; then
    "$python" -m zipfile -c "dist/$name.zip" "$stage"
  else
    tar --owner=0 --group=0 --numeric-owner -C dist/.stage -czf "dist/$name.tar.gz" "$name"
  fi
done

rm -rf dist/.stage
(cd dist && sha256sum instadeck_* > SHA256SUMS)

ls -l dist
