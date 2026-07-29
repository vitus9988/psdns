#!/usr/bin/env bash
# Publish a psdns TEST pre-release: commit the current changes onto the `test`
# branch, push it (runs ci.yml), then tag a vX.Y.Z-rc.N pre-release and push the
# tag (runs release.yml, which publishes it as a GitHub *pre-release* — excluded
# from /releases/latest, so stable users are never auto-updated to it).
# After the tag is pushed, the release run is watched (needs `gh`; skipped
# otherwise): if the run FAILS the rc tag is deleted locally and on origin, so
# the next attempt reuses the same rc number and no orphan tag is left behind.
# A watch timeout or Ctrl-C never deletes the tag.
#
# Usage: scripts/release-test.sh [--dry-run] [--msg "commit message"]
#                                [--no-wait] [--timeout SEC] [VERSION]
#   VERSION    target release like 0.7.0 or v0.7.0. Default: minor-bump the
#              highest stable tag (v0.6.0 -> v0.7.0). The -rc.N auto-increments.
#   --dry-run  print the plan (branch, version, files) and exit, changing nothing.
#   --msg M    commit message (default: a templated message).
#   --no-wait  push the tag and exit without watching the release run.
#   --timeout SEC  seconds to watch the release run (default 1200).
#
# Driven by the /release-test skill, but safe to run by hand. Uses `git tag
# --sort=-v:refname` (not `sort -V`) so version ordering works on macOS too.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
. scripts/release-lib.sh

DRY=0; MSG=""; TARGET=""; NO_WAIT=0; TIMEOUT=1200
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1 ;;
    --msg) [ $# -ge 2 ] || { echo "--msg 값이 필요합니다" >&2; exit 2; }; MSG="$2"; shift ;;
    --msg=*) MSG="${1#--msg=}" ;;
    --no-wait) NO_WAIT=1 ;;
    --timeout) [ $# -ge 2 ] || { echo "--timeout 값이 필요합니다" >&2; exit 2; }; TIMEOUT="$2"; shift ;;
    --timeout=*) TIMEOUT="${1#--timeout=}" ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) TARGET="$1" ;;
  esac
  shift
done
case "$TIMEOUT" in ''|*[!0-9]*) echo "--timeout 은 초 단위 숫자여야 합니다: ${TIMEOUT}" >&2; exit 2 ;; esac

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
if [ "$NO_WAIT" = 1 ]; then
  wait_desc="생략(--no-wait)"
else
  wait_desc="release.yml 감시, 실패 시 rc 태그 자동 회수 (최대 ${TIMEOUT}s)"
fi
echo "── psdns 테스트 프리릴리즈 계획 ───────────────"
echo "  현재 브랜치 : ${cur}  →  test"
echo "  최신 안정태그: ${latest_stable:-(없음)}"
echo "  대상 버전   : ${TARGET}"
echo "  프리릴리즈  : ${TAG}"
echo "  커밋 메시지 : ${MSG}"
echo "  런 감시     : ${wait_desc}"
echo "  커밋 대상   :"
git status --short | sed 's/^/    /'
echo "──────────────────────────────────────────────"
if [ "$DRY" = 1 ]; then echo "[dry-run] 아무것도 변경하지 않았습니다."; exit 0; fi

# 이번 실행이 만들 릴리즈 런만 매칭하도록 push 전에 컷오프 시각을 기록한다 —
# 같은 커밋·같은 rc 번호로 재시도할 때 직전 실패 런을 오매칭해 방금 push 한
# 태그를 회수하는 사고 방지(find_release_run 의 --created 필터).
WATCH_SINCE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

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

echo "✓ 태그 push 완료: ${TAG} — release.yml 이 프리릴리즈를 게시합니다."
echo "  https://github.com/${REPO}/releases/tag/${TAG}"

# --- release.yml 런 감시 + 실패 시 rc 태그 자동 회수 --------------------------
# 실패한 rc 런은 번호만 소모하고 고아 태그를 남긴다(과거 v0.9.0 이 rc.12 까지
# 소진). 런이 실패로 끝나면 태그를 회수해 다음 시도가 같은 rc 번호를 쓰게 한다.
# 정식 태그의 회수 코드는 의도적으로 release-main.sh 에 없다(자동 삭제 금지).
ACTIONS_URL="https://github.com/${REPO}/actions/workflows/release.yml"
if [ "$NO_WAIT" = 1 ]; then
  echo "(--no-wait) 릴리즈 런 감시를 생략합니다: ${ACTIONS_URL}"
  exit 0
fi
if ! gh_ready; then
  echo "⚠ gh 미설치/미인증 — 릴리즈 런 감시를 생략합니다. 수동 확인: ${ACTIONS_URL}"
  exit 0
fi

sha="$(git rev-parse "${TAG}^{commit}")"
run_id="$(find_release_run "$TAG" "$sha" "$WATCH_SINCE" || true)"
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
    assets="$(gh release view "$TAG" -R "$REPO" --json assets --jq '.assets | length' 2>/dev/null || echo '?')"
    [ "$assets" = "7" ] || echo "⚠ 릴리즈 자산 ${assets}/7 — 릴리즈 페이지를 확인하세요."
    echo "✓ ${TAG} 프리릴리즈 게시 + verify 잡(3-OS 실물 검증) 통과:"
    echo "  https://github.com/${REPO}/releases/tag/${TAG}"
    echo "  로컬 실물 확인(선택): bash scripts/verify-release.sh ${TAG}"
    ;;
  20)
    echo "⚠ 감시 타임아웃(${TIMEOUT}s) — 태그는 회수하지 않습니다. 이어서 보려면:"
    echo "  gh run watch ${run_id} -R ${REPO} --exit-status"
    exit 20
    ;;
  3)
    echo "⚠ gh 조회 실패가 반복돼 감시를 중단합니다 — 태그는 회수하지 않습니다: ${ACTIONS_URL}"
    exit 0
    ;;
  *)
    echo "✗ ${TAG} 릴리즈 런 실패:"
    summarize_failure "$run_id"
    echo "  rc 태그를 회수합니다(다음 시도가 같은 번호를 재사용)…"
    # 게시 후반 실패(예: verify 잡) 대비: 릴리즈가 이미 있으면 릴리즈+태그를 함께,
    # 없으면 원격 태그만 지운다.
    if ! gh release delete "$TAG" -R "$REPO" --cleanup-tag --yes >/dev/null 2>&1; then
      git push origin ":refs/tags/${TAG}" \
        || echo "⚠ 원격 태그 삭제 실패 — 수동으로: git push origin :refs/tags/${TAG}"
    fi
    git tag -d "$TAG" >/dev/null 2>&1 || true
    echo "  원인 수정 후 다시 실행하면 같은 ${TAG} 번호로 재시도합니다."
    exit 1
    ;;
esac
