#!/usr/bin/env bash
# Build cross-platform psdns release archives into dist/.
#
# Usage: scripts/build-release.sh [VERSION] [GUI_PLATFORM] [TARGET...]
#   VERSION       defaults to `git describe` (tag/sha) or "dev".
#   GUI_PLATFORM  Wails build platform for the GUI (psdns-gui), one of:
#                   host              build for the current OS/arch (default)
#                   darwin/universal  one .app for both Intel and Apple Silicon
#                   windows/amd64 | linux/amd64 | ...   a specific platform
#                   none              CLI-only, do not build the GUI
#                 The GUI is bundled into every archive whose OS matches (and,
#                 unless the platform is darwin/universal, whose arch matches).
#   TARGET...     archive targets to produce (default: all six). Each is GOOS/GOARCH.
#
# Each archive holds the static CLI binary `psdns` (and, where the GUI was built,
# the desktop app) plus README and LICENSE. The CLI is always CGO-free and
# statically linked; the Wails GUI needs per-OS native builds, so a single run
# can only attach the GUI for one host platform (CI builds every platform).
# Windows ships a .zip; the rest ship a .tar.gz. A checksums file covers all.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
GUI_PLATFORM="${2:-host}"
shift "$(( $# > 2 ? 2 : $# ))" || true
TARGETS=("$@")
if [ "${#TARGETS[@]}" -eq 0 ]; then
  TARGETS=(windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64)
fi

OUT="dist"
PKG="github.com/vitus9988/psdns"
CLI_LDFLAGS="-s -w -X main.version=${VERSION}"
GUI_LDFLAGS="-s -w -X main.version=${VERSION} -X ${PKG}/internal/selfupdate.Version=${VERSION}"

if [ "$GUI_PLATFORM" = host ]; then
  GUI_PLATFORM="$(go env GOOS)/$(go env GOARCH)"
fi

rm -rf "$OUT"
mkdir -p "$OUT"

# --- Build the GUI (Wails) for at most one platform ---------------------------
GUI_OS="" GUI_ARCH="" GUI_ARTIFACT=""
if [ "$GUI_PLATFORM" != none ]; then
  if command -v wails >/dev/null 2>&1; then
    GUI_OS="${GUI_PLATFORM%/*}"
    GUI_ARCH="${GUI_PLATFORM#*/}"
    echo "psdns ${VERSION} — building GUI for ${GUI_PLATFORM} via wails"
    wtags=()
    [ "$GUI_OS" = linux ] && wtags=(-tags webkit2_41)
    # Newer macOS SDKs need UniformTypeIdentifiers linked explicitly.
    [ "$GUI_OS" = darwin ] && export CGO_LDFLAGS="${CGO_LDFLAGS:-} -framework UniformTypeIdentifiers"
    ( cd cmd/psdns-gui && wails build -platform "$GUI_PLATFORM" \
        -ldflags "$GUI_LDFLAGS" -trimpath -clean ${wtags[@]+"${wtags[@]}"} )
    case "$GUI_OS" in
      darwin)  GUI_ARTIFACT="psdns.app" ;;
      windows) GUI_ARTIFACT="psdns-gui.exe" ;;
      *)       GUI_ARTIFACT="psdns-gui" ;;
    esac
  else
    echo "wails CLI not found — building CLI-only archives (set up Wails to include the GUI)."
  fi
fi

# bundles_gui reports whether the GUI artifact belongs in the given target's archive.
bundles_gui() {
  local os="$1" arch="$2"
  [ -n "$GUI_ARTIFACT" ] || return 1
  [ "$os" = "$GUI_OS" ] || return 1
  [ "$GUI_ARCH" = universal ] && return 0
  [ "$arch" = "$GUI_ARCH" ]
}

make_zip() { # $1 = directory name under $OUT
  if command -v zip >/dev/null 2>&1; then ( cd "$OUT" && zip -qr "$1.zip" "$1" )
  else ( cd "$OUT" && 7z a -tzip "$1.zip" "$1" >/dev/null ); fi
}

sha256_over() { # SHA-256 the given files; shasum (macOS) or sha256sum (Linux)
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$@"
  elif command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"
  else echo "no shasum/sha256sum available" >&2; return 1; fi
}

echo "psdns ${VERSION} — packaging ${#TARGETS[@]} target(s)"
for target in "${TARGETS[@]}"; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  bin="psdns"
  [ "$GOOS" = windows ] && bin="psdns.exe"

  name="psdns_${VERSION}_${GOOS}_${GOARCH}"
  stage="${OUT}/${name}"
  mkdir -p "$stage"

  echo "  - ${GOOS}/${GOARCH}"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags "$CLI_LDFLAGS" -o "${stage}/${bin}" ./cmd/psdns
  cp README.md LICENSE "$stage/"

  if bundles_gui "$GOOS" "$GOARCH"; then
    cp -R "cmd/psdns-gui/build/bin/${GUI_ARTIFACT}" "$stage/"
    echo "    (+ GUI: ${GUI_ARTIFACT})"
  fi

  if [ "$GOOS" = windows ]; then make_zip "$name"; else tar -czf "${OUT}/${name}.tar.gz" -C "$OUT" "$name"; fi
  rm -rf "$stage"
done

# Checksums over every archive. In CI each per-OS runner builds only part of the
# matrix, so SKIP_CHECKSUMS=1 there and let the release job write one combined
# file over all collected archives.
if [ "${SKIP_CHECKSUMS:-0}" != 1 ]; then
  ( cd "$OUT" && sha256_over psdns_*.tar.gz psdns_*.zip > "psdns_${VERSION}_checksums.txt" )
fi

echo "done -> ${OUT}/"
ls -1 "$OUT"
