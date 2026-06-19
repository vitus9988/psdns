#!/usr/bin/env bash
# Publish a psdns TEST pre-release: commit the current changes onto the `test`
# branch, push it (runs ci.yml), then tag a vX.Y.Z-rc.N pre-release and push the
# tag (runs release.yml, which publishes it as a GitHub *pre-release* — excluded
# from /releases/latest, so stable users are never auto-updated to it).
#
# Usage: scripts/release-test.sh [--dry-run] [--msg "commit message"] [VERSION]
#   VERSION    target release like 0.7.0 or v0.7.0. Default: minor-bump the
#              highest stable tag (v0.6.0 -> v0.7.0). The -rc.N auto-increments.
#   --dry-run  print the plan (branch, version, files) and exit, changing nothing.
#   --msg M    commit message (default: a templated message).
#
# Driven by the /release-test skill, but safe to run by hand. Uses `git tag
# --sort=-v:refname` (not `sort -V`) so version ordering works on macOS too.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

DRY=0; MSG=""; TARGET=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1 ;;
    --msg) MSG="${2:-}"; shift ;;
    --msg=*) MSG="${1#--msg=}" ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) TARGET="$1" ;;
  esac
  shift
done

norm() { local v="${1#v}"; v="${v#V}"; echo "v${v}"; }

# --- resolve target release + next rc tag ------------------------------------
latest_stable="$(git tag --list 'v[0-9]*' --sort=-v:refname | grep -vE -- '-' | head -1 || true)"
if [ -n "$TARGET" ]; then
  TARGET="$(norm "$TARGET")"
else
  base="${latest_stable:-v0.0.0}"
  IFS=. read -r MA MI _ <<<"${base#v}"
  TARGET="v${MA}.$((MI + 1)).0"
fi
rc_prev="$(git tag --list "${TARGET}-rc.*" --sort=-v:refname | head -1 || true)"
rc_n="$(printf '%s' "$rc_prev" | sed -E 's/.*-rc\.//')"
TAG="${TARGET}-rc.$(( ${rc_n:-0} + 1 ))"
[ -n "$MSG" ] || MSG="chore(test): ${TAG} 후보 빌드"

# --- show the plan -----------------------------------------------------------
cur="$(git symbolic-ref --short HEAD 2>/dev/null || echo DETACHED)"
echo "── psdns 테스트 프리릴리즈 계획 ───────────────"
echo "  현재 브랜치 : ${cur}  →  test"
echo "  최신 안정태그: ${latest_stable:-(없음)}"
echo "  대상 버전   : ${TARGET}"
echo "  프리릴리즈  : ${TAG}"
echo "  커밋 메시지 : ${MSG}"
echo "  커밋 대상   :"
git status --short | sed 's/^/    /'
echo "──────────────────────────────────────────────"
if [ "$DRY" = 1 ]; then echo "[dry-run] 아무것도 변경하지 않았습니다."; exit 0; fi

# --- ensure we are on `test` (carrying the working changes over) -------------
if [ "$cur" != "test" ]; then
  if git show-ref --verify --quiet refs/heads/test; then
    git checkout test
  else
    git checkout -b test
  fi
fi

# --- commit (if dirty), push branch, tag, push tag ---------------------------
if [ -n "$(git status --porcelain)" ]; then
  git add -A
  git commit -m "$MSG"
else
  echo "커밋할 변경이 없어 현재 HEAD로 진행합니다."
fi
git push -u origin test
git tag "$TAG"
git push origin "$TAG"

echo "✓ 완료: ${TAG} 프리릴리즈가 곧 게시됩니다 (Actions → release.yml)."
echo "  https://github.com/vitus9988/psdns/releases/tag/${TAG}"
