#!/usr/bin/env bash
# Build cross-platform psdns release archives into dist/.
#
# Usage: scripts/build-release.sh [VERSION]
#   VERSION defaults to `git describe` (tag/sha) or "dev".
#
# Produces, per target, a self-contained archive holding the static binary plus
# README and LICENSE — extract and run, no toolchain or dependencies required.
# Windows targets ship a .zip; the rest ship a .tar.gz. A checksums file covers
# every archive.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="dist"
LDFLAGS="-s -w -X main.version=${VERSION}"

# GOOS/GOARCH matrix. CGO is disabled so every binary is statically linked and
# runs on a bare OS install.
TARGETS=(
  windows/amd64
  windows/arm64
  darwin/amd64
  darwin/arm64
  linux/amd64
  linux/arm64
)

rm -rf "$OUT"
mkdir -p "$OUT"

echo "psdns ${VERSION} — building ${#TARGETS[@]} targets"

for target in "${TARGETS[@]}"; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  bin="psdns"
  [ "$GOOS" = "windows" ] && bin="psdns.exe"

  name="psdns_${VERSION}_${GOOS}_${GOARCH}"
  stage="${OUT}/${name}"
  mkdir -p "$stage"

  echo "  - ${GOOS}/${GOARCH}"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${stage}/${bin}" ./cmd/psdns
  cp README.md LICENSE "$stage/"

  if [ "$GOOS" = "windows" ]; then
    ( cd "$OUT" && zip -qr "${name}.zip" "$name" )
  else
    tar -czf "${OUT}/${name}.tar.gz" -C "$OUT" "$name"
  fi
  rm -rf "$stage"
done

# Checksums over every archive (shasum is present on both macOS and Linux).
( cd "$OUT" && shasum -a 256 psdns_*.tar.gz psdns_*.zip > "psdns_${VERSION}_checksums.txt" )

echo "done -> ${OUT}/"
ls -1 "$OUT"
