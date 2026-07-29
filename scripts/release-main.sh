#!/usr/bin/env bash
# Promote the tested `test` branch to a production release: merge test into
# main and push (if main is branch-protected and the push is rejected, the
# script falls back to a PR: create → wait for checks → merge; force with
# --pr), then tag the final vX.Y.Z (no suffix) and push it (runs release.yml,
# publishing a FULL release that every user is auto-updated to). Run this only
# AFTER the rc pre-release has been validated on real Windows/macOS.
# The release run is then watched (needs `gh`; skipped otherwise). A FINAL tag
# is NEVER deleted automatically — on failure the script prints `gh run rerun`
# guidance instead. After the stable release is published, release.yml prunes
# that version's -rc pre-releases/tags (scripts/prune-rc.sh, GITHUB_TOKEN — no
# local gh auth needed) and this script then drops the local rc tags too.
#
# Usage: scripts/release-main.sh [--dry-run] [--pr] [--no-wait] [--timeout SEC] [VERSION]
#   VERSION    final version like 0.7.0 or v0.7.0. Default: strip the suffix from
#              the highest -rc tag (v0.7.0-rc.3 -> v0.7.0).
#   --dry-run  print the plan and exit, changing nothing.
#   --pr       promote via a PR even when a direct push would work.
#   --no-wait  skip watching the release run after pushing the tag.
#   --timeout SEC  seconds to watch the release run (default 1200).
#
# Driven by the /release-main skill, but safe to run by hand.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
. scripts/release-lib.sh

DRY=0; FINAL=""; USE_PR=0; NO_WAIT=0; TIMEOUT=1200
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1 ;;
    --pr) USE_PR=1 ;;
    --no-wait) NO_WAIT=1 ;;
    --timeout) [ $# -ge 2 ] || { echo "--timeout 값이 필요합니다" >&2; exit 2; }; TIMEOUT="$2"; shift ;;
    --timeout=*) TIMEOUT="${1#--timeout=}" ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) FINAL="$1" ;;
  esac
  shift
done
case "$TIMEOUT" in ''|*[!0-9]*) echo "--timeout 은 초 단위 숫자여야 합니다: ${TIMEOUT}" >&2; exit 2 ;; esac

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

# 이 버전의 rc — 정식 태그 push 뒤 release.yml(scripts/prune-rc.sh)이 자동
# 정리한다(여기선 표시용). gh 가 있으면 원격 기준으로 게시/고아 태그를 구분해
# 보여 주고(로컬에 없는 rc 포함), 없으면 로컬 태그 목록으로 폴백한다.
if gh_ready; then
  rcs_pub="$(gh release list -R "$REPO" --limit 200 --json tagName,isPrerelease \
      --jq ".[] | select(.isPrerelease and (.tagName | startswith(\"${FINAL}-rc.\"))) | .tagName" 2>/dev/null || true)"
  rcs_all="$(gh api "repos/${REPO}/git/matching-refs/tags/${FINAL}-rc." \
      --jq '.[].ref' 2>/dev/null | sed 's#^refs/tags/##' || true)"
  pub_n="$(printf '%s' "$rcs_pub" | grep -c . || true)"
  all_n="$(printf '%s' "$rcs_all" | grep -c . || true)"
  orph_n=$((all_n - pub_n))
  [ "$orph_n" -ge 0 ] || orph_n=0 # 태그만 수동 삭제된 릴리즈가 있으면 음수 가능(표시용 클램프)
  rcs_display="게시 ${pub_n}개 + 고아 태그 ${orph_n}개: $(echo "${rcs_all:-(없음)}" | tr '\n' ' ')"
else
  rcs_local="$(git tag --list "${FINAL}-rc.*" --sort=-v:refname)"
  rcs_display="(로컬 태그 기준) $(echo "${rcs_local:-(없음)}" | tr '\n' ' ')"
fi

echo "── psdns 정식 릴리즈 계획 ─────────────────────"
echo "  기준 rc 태그 : ${highest_rc:-(없음)}"
echo "  정식 버전    : ${FINAL}"
if [ "$USE_PR" = 1 ]; then
  echo "  동작        : test → main PR 생성·체크 대기·머지(--pr), ${FINAL} 태그 push, test 동기화"
else
  echo "  동작        : test → main 병합(--no-ff, push 거부 시 PR 폴백), main push, ${FINAL} 태그 push, test 동기화"
fi
if [ "$NO_WAIT" = 1 ]; then
  echo "  런 감시     : 생략(--no-wait)"
else
  echo "  런 감시     : release.yml 감시(최대 ${TIMEOUT}s) — 정식 태그는 실패해도 회수하지 않음"
fi
echo "  rc 정리(CI) : ${rcs_display}"
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

# 이번 실행이 만들 릴리즈 런만 매칭하도록 push 전에 컷오프 시각을 기록한다
# (find_release_run 의 --created 필터 — 직전 런 오매칭 방지).
WATCH_SINCE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# --- merge test -> main (직접 push, 보호 시 PR 폴백), tag, push ---------------
git fetch origin

# 직접 경로. 사전 가드 실패(체크아웃 불가·ff 불가·병합 충돌)는 여기서 스크립트를
# 끝내고(exit), 마지막 push 의 거부만 리턴돼 PR 폴백 트리거가 된다 — 브랜치
# 보호와 무관한 상태 이상을 PR 로 우회하지 않기 위한 구분이다.
promote_direct() {
  git checkout main \
    || { echo "main 체크아웃 실패 — 수동 확인이 필요합니다." >&2; exit 1; }
  git merge --ff-only origin/main \
    || { echo "로컬 main이 origin/main과 어긋났습니다 — 수동 확인이 필요합니다." >&2; exit 1; }
  git merge --no-ff test -m "Merge test into main for ${FINAL}" \
    || { echo "test 병합 실패 — 충돌을 수동으로 해결하세요." >&2; git merge --abort >/dev/null 2>&1 || true; exit 1; }
  git push origin main
}

# PR 경로: 보호된 main 용. head=test 의 기존 open PR 을 재사용하고 없으면 만든다.
promote_via_pr() {
  gh_ready || {
    echo "main 직접 push 불가 + gh 사용 불가 — 수동 PR 절차:" >&2
    echo "  gh pr create --base main --head test --fill → CI 통과 후 머지 → git pull 후 태그 push" >&2
    exit 1
  }
  git push origin test
  pr="$(gh pr list -R "$REPO" --head test --base main --state open \
          --json number --jq '.[0].number' 2>/dev/null || true)"
  if [ -z "$pr" ]; then
    pr="$(gh pr create -R "$REPO" --base main --head test \
            --title "Merge test into main for ${FINAL}" \
            --body "정식 릴리즈 ${FINAL} 승격 병합 (release-main.sh 자동 생성)." \
          | grep -oE '[0-9]+$' || true)"
  fi
  [ -n "$pr" ] || { echo "✗ PR 생성/조회 실패 — 수동으로: gh pr create --base main --head test --fill" >&2; exit 1; }
  echo "PR #${pr} 체크 대기 중… (https://github.com/${REPO}/pull/${pr})"
  gh pr checks "$pr" -R "$REPO" --watch --fail-fast \
    || { echo "✗ PR 체크 실패 — 원인 수정 후 다시 실행하세요: https://github.com/${REPO}/pull/${pr}" >&2; exit 1; }
  gh pr merge "$pr" -R "$REPO" --merge --subject "Merge test into main for ${FINAL}" \
    || { echo "✗ PR 머지 실패 — 수동 확인: https://github.com/${REPO}/pull/${pr}" >&2; exit 1; }
  git fetch origin
  # 병합 직후엔 origin/main 이 진실이다 — 로컬 main 이 어긋나 있어도(--pr 직행 등)
  # 강제로 맞춰서, 원격은 병합됐는데 태그를 못 다는 중간 상태를 만들지 않는다.
  git checkout -B main origin/main
}

if [ "$USE_PR" = 1 ]; then
  promote_via_pr
elif ! promote_direct; then
  echo "main 직접 push 가 거부됐습니다(브랜치 보호 추정) — PR 경로로 전환합니다."
  # 미푸시 로컬 병합 커밋을 원격 상태로 되돌린다. 체크아웃 중인 브랜치는
  # branch -f 할 수 없으므로 먼저 test 로 이동한다.
  git checkout test
  git branch -f main origin/main
  promote_via_pr
fi

git tag "$FINAL"
git push origin "$FINAL"
# Keep test aligned with main so the next cycle starts clean.
git checkout test
git merge --ff-only main
git push origin test

echo "✓ 태그 push 완료: ${FINAL} — release.yml 이 정식 릴리즈를 게시합니다."
echo "  https://github.com/${REPO}/releases/tag/${FINAL}"
echo "  (게시 후 release.yml 이 ${FINAL}-rc.* 프리릴리즈/태그를 자동 정리합니다: scripts/prune-rc.sh)"
echo "  (현재 브랜치: test — 다음 작업을 바로 이어서 진행할 수 있습니다.)"

# --- release.yml 런 감시 (정식 태그는 실패해도 절대 자동 삭제하지 않는다) ------
ACTIONS_URL="https://github.com/${REPO}/actions/workflows/release.yml"
if [ "$NO_WAIT" = 1 ]; then
  echo "(--no-wait) 릴리즈 런 감시를 생략합니다: ${ACTIONS_URL}"
  exit 0
fi
if ! gh_ready; then
  echo "⚠ gh 미설치/미인증 — 릴리즈 런 감시를 생략합니다. 수동 확인: ${ACTIONS_URL}"
  exit 0
fi

sha="$(git rev-parse "${FINAL}^{commit}")"
run_id="$(find_release_run "$FINAL" "$sha" "$WATCH_SINCE" || true)"
if [ -z "$run_id" ]; then
  echo "⚠ 릴리즈 런을 찾지 못했습니다(최대 120s 대기) — 감시를 생략합니다: ${ACTIONS_URL}"
  exit 0
fi
echo "릴리즈 런 감시 중 (run ${run_id}, 최대 ${TIMEOUT}s):"
echo "  https://github.com/${REPO}/actions/runs/${run_id}"

watch_rc=0
watch_run "$run_id" "$TIMEOUT" || watch_rc=$?
case "$watch_rc" in
  0)
    echo "✓ ${FINAL} 정식 릴리즈 게시 + verify 잡(3-OS 실물 검증) 통과:"
    echo "  https://github.com/${REPO}/releases/tag/${FINAL}"
    # 원격 rc 는 CI(prune-rc.sh)가 지웠으므로 로컬 rc 태그도 정리해 다음 사이클의
    # rc 번호 계산(release-test.sh)과 정합을 맞춘다.
    local_rcs="$(git tag --list "${FINAL}-rc.*")"
    if [ -n "$local_rcs" ]; then
      echo "$local_rcs" | while IFS= read -r t; do
        [ -n "$t" ] || continue
        git tag -d "$t" >/dev/null 2>&1 || true
      done
      echo "  로컬 rc 태그 정리: $(echo "$local_rcs" | tr '\n' ' ')"
    fi
    ;;
  20)
    echo "⚠ 감시 타임아웃(${TIMEOUT}s) — 게시 여부를 확인하세요:"
    echo "  gh run watch ${run_id} -R ${REPO} --exit-status"
    exit 20
    ;;
  3)
    echo "⚠ gh 조회 실패가 반복돼 감시를 중단합니다: ${ACTIONS_URL}"
    exit 0
    ;;
  *)
    echo "✗ ${FINAL} 릴리즈 런 실패:"
    summarize_failure "$run_id"
    echo "  정식 태그는 main 에 이미 병합돼 있어 회수하지 않습니다. 조치:"
    echo "   - 일시 오류(러너 등)면: gh run rerun ${run_id} -R ${REPO} --failed"
    echo "   - 코드 문제면: 수정 → /release-test 검증 → 다음 정식 버전으로 재시도"
    exit 1
    ;;
esac
