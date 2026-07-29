#!/usr/bin/env bash
# psdns 릴리즈 스크립트 공용 gh 헬퍼. release-test.sh / release-main.sh /
# verify-release.sh / prune-rc.sh 가 source 해서 쓴다 (직접 실행하지 않음).
#
# gh(GitHub CLI)가 없거나 미인증이어도 호출부가 현행 동작으로 폴백할 수 있도록
# 모든 함수는 실패를 리턴코드로만 알리고 스크립트를 중단시키지 않는다.
# bash 3.2(macOS 기본) 호환 유지: mapfile·연관배열·`${v,,}` 사용 금지.

# PSDNS_REPO 로 명시 오버라이드 > CI 의 GITHUB_REPOSITORY > 기본값.
REPO="${PSDNS_REPO:-${GITHUB_REPOSITORY:-vitus9988/psdns}}"

# gh 존재 + 인증 여부 (GH_TOKEN 환경변수 인증 포함). 감시/조회 가능성 판별용.
gh_ready() {
  command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1
}

# 태그 push 가 트리거한 release.yml 런의 ID 를 찾는다.
# $1=태그  $2=커밋 SHA  $3=(선택) 생성 컷오프(ISO8601 UTC, 예 2026-07-23T06:00:00Z).
# push 직후 런 생성이 몇 초 지연되므로 5s 간격으로 최대 120s 폴링한다.
# 태그 push 런의 headBranch 는 태그명이지만, 실패한 rc 태그를 회수한 뒤 같은
# 이름을 재사용하면 이전 실패 런도 같은 headBranch 로 남으므로 --commit 으로
# 이번 push 의 커밋을 특정하고(목록은 최신순 → .[0]), **커밋 변경 없는 재시도**
# (같은 SHA·같은 태그 번호)는 --commit 으로도 못 가르므로 호출부가 push 전에
# 기록한 컷오프($3)로 `--created >=` 필터를 걸어 직전 실패 런 오매칭을 막는다.
# 로컬 시계가 GitHub 보다 앞서면 새 런이 필터에 걸려 못 찾을 수 있는데, 그 경우
# 호출부는 감시만 생략(exit 0)하므로 태그 오삭제 방향으로는 실패하지 않는다.
find_release_run() {
  local tag="$1" sha="$2" since="${3:-}" id="" i=0
  local created=()
  if [ -n "$since" ]; then created=(--created ">=${since}"); fi
  while [ "$i" -lt 24 ]; do
    id="$(gh run list -R "$REPO" --workflow=release.yml --event=push \
            --branch "$tag" --commit "$sha" ${created[@]+"${created[@]}"} --limit 1 \
            --json databaseId --jq '.[0].databaseId' 2>/dev/null || true)"
    if [ -n "$id" ]; then echo "$id"; return 0; fi
    sleep 5; i=$((i + 1))
  done
  return 1
}

# 런이 끝날 때까지 10s 간격으로 폴링한다. $1=run ID, $2=타임아웃 초(기본 1200).
# 리턴: 0=성공  1=실패(failure/cancelled 등)  20=타임아웃  3=gh 조회 불능(연속 3회).
# `gh run watch` 를 쓰지 않는 이유: 타임아웃이 없고(macOS 엔 timeout(1)도 없음),
# 폴링 루프는 완료된 과거 런으로 푸시 없이 단위 검증할 수 있다.
# Ctrl-C 는 감시만 중단한다(런은 계속 진행) — 태그 회수 같은 후속 동작 없이 130 종료.
watch_run() {
  local id="$1" timeout="${2:-1200}" deadline st="" misses=0
  deadline=$(( $(date +%s) + timeout ))
  trap 'echo ""; echo "감시 중단(Ctrl-C) — 런은 계속 실행 중이며 태그는 회수하지 않습니다."; echo "  https://github.com/'"$REPO"'/actions/runs/'"$id"'"; trap - INT; exit 130' INT
  while [ "$(date +%s)" -lt "$deadline" ]; do
    st="$(gh run view "$id" -R "$REPO" --json status,conclusion \
            --jq '.status + "/" + (.conclusion // "")' 2>/dev/null || true)"
    if [ -z "$st" ]; then
      misses=$((misses + 1))
      if [ "$misses" -ge 3 ]; then trap - INT; return 3; fi
    else
      misses=0
      case "$st" in
        completed/success) trap - INT; return 0 ;;
        completed/*)       trap - INT; return 1 ;; # failure|cancelled|timed_out…
        *) printf '  … 릴리즈 런 진행 중 (%s)\n' "${st%%/*}" ;;
      esac
    fi
    sleep 10
  done
  trap - INT
  return 20
}

# 실패한 런의 실패 잡 이름을 한 줄로 요약하고 상세 로그 명령을 안내한다. $1=run ID.
summarize_failure() {
  local id="$1" jobs=""
  jobs="$(gh run view "$id" -R "$REPO" --json jobs \
            --jq '[.jobs[] | select(.conclusion == "failure") | .name] | join(", ")' 2>/dev/null || true)"
  echo "  실패한 잡: ${jobs:-(조회 실패)}"
  echo "  상세 로그: gh run view ${id} -R ${REPO} --log-failed"
}
