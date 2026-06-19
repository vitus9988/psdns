---
name: release-main
description: psdns 정식 릴리즈 게시 — 검증된 test 브랜치를 main에 병합하고 vX.Y.Z(접미사 없는) 정식 버전을 릴리즈한다. "정식 배포", "main 릴리즈", "프로덕션 릴리즈", "정식 버전 배포" 요청 시 사용.
---

# psdns 정식 릴리즈 (`/release-main`)

프리릴리즈 검증을 마친 뒤 `test` 를 `main` 에 병합하고, **모든 사용자에게 자동 업데이트로 제안되는** 정식 릴리즈(`vX.Y.Z`)를 게시한다. 결정적 작업은 엔진 스크립트 `scripts/release-main.sh` 가 수행한다.

## 사전 조건 (먼저 확인)
- `/release-test` 로 만든 rc 빌드를 **실제로 테스트**해 이상이 없다고 사용자가 확인했는가? 확인 전이면 진행하지 말고 먼저 검증을 권한다.
- 작업트리가 **깨끗**한가(미커밋 변경 없음)? 변경이 남아 있으면 먼저 `/release-test` 로 반영하거나 정리한다.

## 절차
1. **계획 미리보기(필수)** — `bash scripts/release-main.sh --dry-run` 으로 기준 rc 태그·정식 버전·동작(병합/푸시/태그)을 보여준다. 버전을 지정하려면 `bash scripts/release-main.sh --dry-run 0.7.0`.
2. **확인(필수)** — 이 작업은 **모든 사용자에게 자동 업데이트로 배포**되는 외부 공개 작업이다. 정식 버전 번호를 사용자에게 확인받고 **명시적 동의**를 받은 뒤에만 실행한다.
3. **실행** — `bash scripts/release-main.sh [버전]`.
4. **보고** — 게시된 정식 릴리즈 URL을 보고한다.

## 동작 규칙
- 인자 없으면 가장 높은 `-rc` 태그에서 접미사를 떼어 정식 버전을 정한다(`v0.7.0-rc.3`→`v0.7.0`).
- 스크립트는 `main` 을 origin과 맞춘 뒤 `test` 를 `--no-ff` 로 병합하고 정식 태그를 push하며, 끝으로 `test` 를 `main` 에 맞춰 다음 사이클을 준비한다.
- **브랜치 보호로 main 직접 push가 막히면** 스크립트가 멈춘다. 그때는 GitHub에서 PR로 병합하도록 안내한다: `gh pr create --base main --head test --fill` → CI 통과 후 머지 → `main` 에서 `git pull` 후 정식 태그만 push(`git tag vX.Y.Z && git push origin vX.Y.Z`).
- 이미 같은 정식 태그가 있으면 스크립트가 중단한다(중복 릴리즈 방지).
