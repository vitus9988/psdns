#!/usr/bin/env bash
# Delete one version line's -rc pre-releases and tags, in two passes:
#   1) published rc pre-releases → `gh release delete --cleanup-tag`
#   2) orphan rc tags (a failed rc run leaves a tag with no release, which
#      `gh release list` never returns) → delete the tag refs directly
# Best-effort: individual failures only warn — release.yml calls this right
# after publishing a stable release, and a stale rc must never turn an
# already-published release red. Also usable locally (gh auth) to clean up an
# abandoned version line, e.g.: scripts/prune-rc.sh --dry-run v0.9.0
#
# Usage: scripts/prune-rc.sh [--dry-run] [--keep-local] VERSION
#   VERSION       version line like v0.9.0 (vX.Y.Z-rc.N is normalized to its line).
#   --dry-run     print what would be deleted without deleting anything.
#   --keep-local  keep local rc tags (always kept when running in CI).
set -euo pipefail
. "$(cd "$(dirname "$0")" && pwd)/release-lib.sh"

DRY=0; KEEP_LOCAL=0; VER=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1 ;;
    --keep-local) KEEP_LOCAL=1 ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) VER="$1" ;;
  esac
  shift
done
[ -n "$VER" ] || { echo "버전을 지정하세요 (예: v0.9.0). 자세히: --help" >&2; exit 2; }

gh_ready || { echo "gh(GitHub CLI) 인증이 필요합니다 (gh auth login 또는 GH_TOKEN)." >&2; exit 2; }

base="v${VER#v}"
base="${base%%-rc.*}" # v0.9.0-rc.3 → v0.9.0
prefix="${base}-rc."

run() { if [ "$DRY" = 1 ]; then echo "  [dry-run] $*"; else "$@"; fi; }

echo "정식 ${base} 기준 — ${prefix}* 프리릴리즈/태그를 정리합니다. (repo: ${REPO})"
if [ "$DRY" = 1 ]; then
  echo "[dry-run] 실제로 삭제하지 않습니다. 1차가 지우지 않으므로 2차(고아 태그) 목록에는 게시된 rc 태그도 함께 나옵니다."
fi

# 1) 게시된 rc 프리릴리즈 삭제 (--cleanup-tag 로 태그도 함께 삭제)
if rcs=$(gh release list -R "${REPO}" --limit 200 \
           --json tagName \
           --jq ".[] | select(.tagName | startswith(\"${prefix}\")) | .tagName"); then
  if [ -z "${rcs}" ]; then
    echo "정리할 ${prefix}* 프리릴리즈가 없습니다."
  else
    echo "${rcs}" | while IFS= read -r rc; do
      [ -n "${rc}" ] || continue
      echo "릴리즈 삭제: ${rc}"
      run gh release delete "${rc}" -R "${REPO}" --cleanup-tag --yes \
        || echo "::warning::${rc} 릴리즈 삭제 실패(또는 이미 없음) — 건너뜁니다."
    done
  fi
else
  echo "::warning::rc 프리릴리즈 목록 조회 실패 — 릴리즈 정리를 건너뜁니다."
fi

# 2) 릴리즈 없이 남은 고아 rc 태그 삭제 (실패한 rc 런이 남긴 태그)
if orphans=$(gh api --paginate "repos/${REPO}/git/matching-refs/tags/${prefix}" --jq '.[].ref'); then
  if [ -z "${orphans}" ]; then
    echo "정리할 ${prefix}* 고아 태그가 없습니다."
  else
    echo "${orphans}" | while IFS= read -r ref; do
      [ -n "${ref}" ] || continue
      tag="${ref#refs/tags/}"
      echo "고아 태그 삭제: ${tag}"
      run gh api -X DELETE "repos/${REPO}/git/refs/tags/${tag}" \
        || echo "::warning::${tag} 태그 삭제 실패 — 건너뜁니다."
    done
  fi
else
  echo "::warning::rc 태그 목록 조회 실패 — 태그 정리를 건너뜁니다."
fi

# 3) 로컬 rc 태그 정리 — 다음 사이클의 rc 번호 계산(release-test.sh)과 정합 유지.
#    CI(GITHUB_ACTIONS)는 원격만 다루므로 생략, --keep-local 로 억제 가능.
if [ "$KEEP_LOCAL" != 1 ] && [ -z "${GITHUB_ACTIONS:-}" ]; then
  if locals="$(git tag --list "${prefix}*" 2>/dev/null)" && [ -n "$locals" ]; then
    echo "$locals" | while IFS= read -r t; do
      [ -n "$t" ] || continue
      echo "로컬 태그 삭제: ${t}"
      run git tag -d "$t" || true
    done
  fi
fi

echo "✓ ${prefix}* 정리 완료"
