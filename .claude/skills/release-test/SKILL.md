---
name: release-test
description: psdns 테스트 프리릴리즈 게시 — 현재 수정사항을 test 브랜치에 커밋·푸시하고 vX.Y.Z-rc.N 프리릴리즈를 릴리즈한다. "테스트 릴리즈", "rc 배포", "프리릴리즈", "테스트 버전 배포" 요청 시 사용.
---

# psdns 테스트 프리릴리즈 (`/release-test`)

현재 작업 중인 수정사항을 `test` 브랜치에 올리고, 실물 검증용 **프리릴리즈**(`vX.Y.Z-rc.N`)를 게시한다. 결정적 작업은 엔진 스크립트 `scripts/release-test.sh` 가 수행한다.

## 절차
1. **변경 확인** — `git status -s` 와 `git diff --stat` 으로 무엇이 커밋될지 확인한다. 의도치 않은 파일이 섞여 있으면 사용자에게 알리고 정리한다.
2. **계획 미리보기(필수)** — `bash scripts/release-test.sh --dry-run` 을 실행해 대상 버전·프리릴리즈 태그·브랜치·커밋 목록을 보여준다. 특정 버전이 필요하면 `bash scripts/release-test.sh --dry-run 0.7.0` 처럼 인자를 붙인다.
3. **커밋 메시지 작성** — 변경 내용으로 간결한 Conventional Commit 메시지를 만든다(예: `fix(proxy): Windows 포트 폴백`).
4. **확인(필수)** — push·태그는 **외부 공개** 작업이다. dry-run 결과와 커밋 메시지를 사용자에게 보여주고 **명시적 동의**를 받은 뒤에만 다음으로 넘어간다.
5. **실행** — 동의를 받으면 `bash scripts/release-test.sh --msg "<메시지>" [버전]` 을 실행한다.
6. **보고 & 안내** — 게시된 프리릴리즈 URL을 보고하고, **실제 Windows/macOS에서 받아 테스트**하도록 안내한다(현재 macOS는 미서명이라 `xattr -dr com.apple.quarantine psdns.app` 로 우회). 이상이 없으면 `/release-main` 으로 정식 배포하면 된다고 알린다.

## 동작 규칙
- 인자 없이 실행하면 최신 안정 태그에서 마이너 버전을 올리고(`v0.6.0`→`v0.7.0`) `-rc.1` 부터 시작하며, 이미 있으면 `-rc.N` 을 자동 증가시킨다.
- 스크립트는 `test` 브랜치가 없으면 현재 브랜치에서 만들고, 있으면 전환한다. 전환이 충돌하면 `set -e` 로 멈추므로, 그때는 `git status` 를 보여주고 사용자가 해결하도록 돕는다(임의로 stash/reset 하지 말 것).
- 한 사이클에서 버그를 더 고쳐 다시 `/release-test` 를 부르면 같은 대상 버전의 다음 `-rc.N` 으로 올라간다.
