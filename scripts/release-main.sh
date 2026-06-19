#!/usr/bin/env bash
# Promote the tested `test` branch to a production release: merge test into main,
# push main, then tag the final vX.Y.Z (no suffix) and push it (runs release.yml,
# publishing a FULL release that every user is auto-updated to). Run this only
# AFTER the rc pre-release has been validated on real Windows/macOS.
#
# Usage: scripts/release-main.sh [--dry-run] [VERSION]
#   VERSION    final version like 0.7.0 or v0.7.0. Default: strip the suffix from
#              the highest -rc tag (v0.7.0-rc.3 -> v0.7.0).
#   --dry-run  print the plan and exit, changing nothing.
#
# Driven by the /release-main skill, but safe to run by hand.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

DRY=0; FINAL=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) FINAL="$1" ;;
  esac
  shift
done

norm() { local v="${1#v}"; v="${v#V}"; echo "v${v}"; }

highest_rc="$(git tag --list 'v*-rc.*' --sort=-v:refname | head -1 || true)"
if [ -n "$FINAL" ]; then
  FINAL="$(norm "$FINAL")"
elif [ -n "$highest_rc" ]; then
  FINAL="${highest_rc%-rc.*}"
else
  echo "정식 버전을 결정할 수 없습니다(-rc 태그 없음). VERSION 인자를 주세요." >&2
  exit 1
fi

echo "── psdns 정식 릴리즈 계획 ─────────────────────"
echo "  기준 rc 태그 : ${highest_rc:-(없음)}"
echo "  정식 버전    : ${FINAL}"
echo "  동작        : test → main 병합(--no-ff), main push, ${FINAL} 태그 push, test 동기화"
echo "──────────────────────────────────────────────"
if [ "$DRY" = 1 ]; then echo "[dry-run] 아무것도 변경하지 않았습니다."; exit 0; fi

# --- guardrails --------------------------------------------------------------
if git rev-parse -q --verify "refs/tags/${FINAL}" >/dev/null; then
  echo "이미 ${FINAL} 태그가 있습니다. 중단합니다." >&2; exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "작업트리에 커밋되지 않은 변경이 있습니다 — 정식 릴리즈는 커밋된 상태에서만 진행하세요." >&2
  echo "  먼저 /release-test 로 변경을 반영하거나 정리한 뒤 다시 실행하세요." >&2
  exit 1
fi

# --- merge test -> main, tag, push, then realign test ------------------------
git fetch origin
git checkout main
git merge --ff-only origin/main || { echo "로컬 main이 origin/main과 어긋났습니다 — 수동 확인이 필요합니다." >&2; exit 1; }
git merge --no-ff test -m "Merge test into main for ${FINAL}"
git push origin main
git tag "$FINAL"
git push origin "$FINAL"
# Keep test aligned with main so the next cycle starts clean.
git checkout test
git merge --ff-only main
git push origin test

echo "✓ 완료: ${FINAL} 정식 릴리즈가 곧 게시됩니다 (Actions → release.yml)."
echo "  https://github.com/vitus9988/psdns/releases/tag/${FINAL}"
echo "  (현재 브랜치: test — 다음 작업을 바로 이어서 진행할 수 있습니다.)"
