#!/usr/bin/env bash
# Print Korean Markdown release notes for TAG to stdout, grouping commit
# subjects by their conventional-commit prefix (feat/fix/…). Used by
# release.yml as the release body (body_path); safe to preview locally.
#
# Range anchor: a stable vX.Y.Z diffs against the previous stable tag; a
# vX.Y.Z-rc.N diffs against the closest EXISTING earlier rc on the same line
# (failed rc tags get recycled, so numbers may have gaps), else the latest
# stable tag. With no anchor at all, the tag's full history is used.
#
# Usage: scripts/release-notes.sh TAG
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

TAG="${1:-}"
[ -n "$TAG" ] || { echo "usage: scripts/release-notes.sh TAG" >&2; exit 2; }
git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null \
  || { echo "태그가 없습니다: ${TAG}" >&2; exit 1; }

# --- 직전 앵커 태그 결정 --------------------------------------------------------
prev=""
case "$TAG" in
  *-rc.*)
    line="${TAG%%-rc.*}"
    prev="$(git tag --list "${line}-rc.*" --sort=-v:refname \
              | awk -v t="$TAG" 'found { print; exit } $0 == t { found=1 }' || true)"
    if [ -z "$prev" ]; then
      prev="$(git tag --list 'v[0-9]*' --sort=-v:refname | grep -vE -- '-' | head -1 || true)"
    fi
    ;;
  *)
    prev="$(git tag --list 'v[0-9]*' --sort=-v:refname | grep -vE -- '-' \
              | awk -v t="$TAG" 'found { print; exit } $0 == t { found=1 }' || true)"
    ;;
esac

if [ -n "$prev" ]; then
  range="${prev}..${TAG}"
  echo "## 변경 사항 (${prev} → ${TAG})"
else
  range="$TAG"
  echo "## 변경 사항 (${TAG})"
fi
echo ""

subjects="$(git log --no-merges --pretty='%s' "$range")"
if [ -z "$subjects" ]; then
  echo "- (변경 커밋 없음)"
  exit 0
fi

# conventional prefix 별 섹션 출력. bash 3.2 호환: 연관배열 대신 프리픽스별 grep 패스.
section() { # $1=섹션 제목  $2=프리픽스 ERE (예: 'feat', 'ci|build')
  local body
  body="$(printf '%s\n' "$subjects" | grep -E "^($2)(\([^)]*\))?!?: " || true)"
  [ -n "$body" ] || return 0
  printf '### %s\n\n' "$1"
  printf '%s\n' "$body" \
    | sed -E "s/^($2)\(([^)]*)\)!?: */- **\2**: /; s/^($2)!?: */- /"
  echo ""
}

section "✨ 기능" 'feat'
section "🐛 수정" 'fix'
section "⚡ 성능" 'perf'
section "♻️ 리팩터링" 'refactor'
section "✅ 테스트" 'test'
section "⚙️ CI·빌드" 'ci|build'
section "📝 문서" 'docs'
section "🧹 기타" 'chore'

# 프리픽스가 없는 커밋도 버리지 않는다.
others="$(printf '%s\n' "$subjects" \
  | grep -vE '^(feat|fix|perf|refactor|test|ci|docs|build|chore)(\([^)]*\))?!?: ' || true)"
if [ -n "$others" ]; then
  printf '### 기타 변경\n\n'
  printf '%s\n' "$others" | sed 's/^/- /'
  echo ""
fi
