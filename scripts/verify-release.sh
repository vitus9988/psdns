#!/usr/bin/env bash
# Verify a published psdns release "for real": check the asset list (6
# archives + checksums), download THIS host's OS/arch archive plus
# checksums.txt, verify its SHA-256, extract, and smoke-test the CLI
# (`psdns version` must equal the tag, then one HTTPS request through
# `psdns proxy`). Read-only against the release — safe to run repeatedly.
# Needs `gh` (logged in, or GH_TOKEN). Shared by release.yml's `verify` job
# (3-OS matrix) and local runs.
#
# Usage: scripts/verify-release.sh [--no-smoke] [TAG]
#   TAG         release tag like v0.9.0 or v0.9.0-rc.12. Default: the most
#               recently published release (pre-releases included).
#   --no-smoke  skip the proxy smoke test (checksum + version still verified).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
. scripts/release-lib.sh

NO_SMOKE=0; TAG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --no-smoke) NO_SMOKE=1 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) TAG="$1" ;;
  esac
  shift
done

gh_ready || { echo "gh(GitHub CLI) 인증이 필요합니다 (gh auth login 또는 GH_TOKEN)." >&2; exit 2; }

if [ -z "$TAG" ]; then
  TAG="$(gh release list -R "$REPO" --limit 1 --json tagName --jq '.[0].tagName')"
  [ -n "$TAG" ] || { echo "릴리즈를 찾지 못했습니다." >&2; exit 1; }
fi
TAG="v${TAG#v}"

echo "── psdns 릴리즈 실물 검증: ${TAG} (repo: ${REPO}) ──"

# --- 1) 자산 목록 검증: 아카이브 6 + checksums 1 = 정확히 7개 ------------------
expected="psdns_${TAG}_checksums.txt
psdns_${TAG}_darwin_amd64.tar.gz
psdns_${TAG}_darwin_arm64.tar.gz
psdns_${TAG}_linux_amd64.tar.gz
psdns_${TAG}_linux_arm64.tar.gz
psdns_${TAG}_windows_amd64.zip
psdns_${TAG}_windows_arm64.zip"
actual=""
for i in 1 2 3; do # 게시 직후 자산 전파 지연 대비 재시도(다운로드와 동일)
  if actual="$(gh release view "$TAG" -R "$REPO" --json assets --jq '.assets[].name' 2>/dev/null | LC_ALL=C sort)" \
     && [ -n "$actual" ] && [ "$actual" = "$expected" ]; then
    break
  fi
  [ "$i" = 3 ] || { echo "  자산 목록 조회 재시도(${i}/3)…"; sleep 10; }
done
if [ "$actual" != "$expected" ]; then
  echo "✗ 자산 목록이 기대와 다릅니다:" >&2
  echo "  ── 기대:" >&2; echo "$expected" | sed 's/^/    /' >&2
  echo "  ── 실제:" >&2; echo "${actual:-(없음)}" | sed 's/^/    /' >&2
  exit 1
fi
echo "✓ 자산 7개 확인 (아카이브 6 + checksums)"

# --- 2) 호스트 OS/arch 아카이브 + checksums 다운로드 ---------------------------
os="$(uname -s)"
case "$os" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  MINGW*|MSYS*|CYGWIN*) os=windows ;;
  *) echo "지원하지 않는 OS: $os" >&2; exit 1 ;;
esac
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "지원하지 않는 아키텍처: $arch" >&2; exit 1 ;;
esac
ext=tar.gz
[ "$os" = windows ] && ext=zip
name="psdns_${TAG}_${os}_${arch}.${ext}"
sums="psdns_${TAG}_checksums.txt"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/psdns-verify.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

got=0
for i in 1 2 3; do # 게시 직후 자산 전파 지연 대비 재시도
  if gh release download "$TAG" -R "$REPO" -D "$tmp" --clobber -p "$name" -p "$sums"; then
    got=1; break
  fi
  echo "  다운로드 재시도(${i}/3)…"; sleep 10
done
[ "$got" = 1 ] || { echo "✗ 자산 다운로드 실패: ${name}" >&2; exit 1; }
echo "✓ 다운로드: ${name}"

# --- 3) SHA-256 대조 (shasum 없으면 sha256sum — Windows Git Bash 대비) ---------
line="$(grep " ${name}\$" "$tmp/$sums")" \
  || { echo "✗ checksums 에 ${name} 항목이 없습니다." >&2; exit 1; }
if command -v shasum >/dev/null 2>&1; then
  (cd "$tmp" && echo "$line" | shasum -a 256 -c -) \
    || { echo "✗ SHA-256 불일치: ${name}" >&2; exit 1; }
else
  (cd "$tmp" && echo "$line" | sha256sum -c -) \
    || { echo "✗ SHA-256 불일치: ${name}" >&2; exit 1; }
fi
echo "✓ SHA-256 일치"

# --- 4) 해제 -------------------------------------------------------------------
dir="$tmp/psdns_${TAG}_${os}_${arch}"
if [ "$ext" = zip ]; then
  if command -v unzip >/dev/null 2>&1; then
    unzip -q "$tmp/$name" -d "$tmp"
  else
    (cd "$tmp" && 7z x -y "$name" >/dev/null) # windows-latest 러너에 7z 보장
  fi
else
  tar -xzf "$tmp/$name" -C "$tmp"
fi
bin="$dir/psdns"
[ "$os" = windows ] && bin="$dir/psdns.exe"
[ -f "$bin" ] || { echo "✗ 아카이브에 CLI 바이너리가 없습니다: $(basename "$bin")" >&2; exit 1; }
chmod +x "$bin" 2>/dev/null || true

# --- 5) 버전 스모크: 주입된 버전이 태그와 정확히 일치해야 한다 ------------------
out="$("$bin" version)"
if [ "$out" != "psdns ${TAG}" ]; then
  echo "✗ version 불일치: got '${out}', want 'psdns ${TAG}'" >&2
  exit 1
fi
echo "✓ version 일치: ${out}"

# --- 6) 프록시 스모크: DoH 해석 + CONNECT 로 HTTPS 1회 --------------------------
if [ "$NO_SMOKE" = 1 ]; then
  echo "(--no-smoke) 프록시 스모크를 생략합니다."
  echo "✓ ${TAG} 실물 검증 통과 (${os}/${arch})"
  exit 0
fi
"$bin" proxy -http 127.0.0.1:18080 -socks 127.0.0.1:11080 >/dev/null 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; rm -rf "$tmp"' EXIT
ok=0
for i in 1 2 3 4 5 6 7 8 9 10; do
  if curl -fsS --max-time 15 --proxy http://127.0.0.1:18080 \
       https://www.gstatic.com/generate_204 >/dev/null 2>&1; then
    ok=1; break
  fi
  sleep 2
done
kill "$pid" 2>/dev/null || true
[ "$ok" = 1 ] || { echo "✗ 프록시 스모크 실패 (proxy 경유 HTTPS 요청 불가)" >&2; exit 1; }
echo "✓ 프록시 스모크 통과 (CONNECT + DoH 해석)"
echo "✓ ${TAG} 실물 검증 통과 (${os}/${arch})"
