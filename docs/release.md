# 릴리즈 절차

버전 태그를 push하면 GitHub Actions(`.github/workflows/release.yml`)가 **OS별 러너**에서 빌드해 6개 아카이브를 Release로 자동 게시합니다. mainstream 4개 타깃(macOS amd64·arm64, Windows amd64, Linux amd64)에는 CLI와 GUI가 함께, ARM 2개 타깃(Windows/Linux arm64)에는 CLI만 담깁니다. (CLI는 `CGO_ENABLED=0`으로 어디서나 크로스 컴파일되지만, Wails GUI는 OS별 네이티브 빌드가 필요해 매트릭스를 씁니다.)

```sh
git tag v1.0.0
git push origin v1.0.0
```

로컬에서 만들려면 `scripts/build-release.sh [버전]` → `dist/`. CLI 6타깃은 항상 빌드되고, GUI는 (Wails CLI가 있으면) **빌드를 실행한 호스트 OS용 아카이브에만** 포함됩니다. 전체 플랫폼 GUI는 CI에서 생성됩니다.

## 테스트 채널 (프리릴리즈)

서명·포트·패키징 같은 산출물 전용 문제는 실제 릴리즈에서만 드러나므로, 정식 배포 전에 **프리릴리즈로 실물을 검증**합니다.

1. 수정사항을 `test` 브랜치에 push → `ci.yml`(gofmt·vet·lint·테스트·커버리지·GUI 테스트)이 돕니다.
2. 프리릴리즈 태그를 push → `release.yml`이 6개 아카이브를 **프리릴리즈**로 게시하고, 이어서 `verify` 잡이 3개 OS 러너에서 산출물을 내려받아 체크섬과 CLI 동작(버전 일치·프록시 스모크)을 **자동 검증**합니다. GUI 동작은 실제 Windows/macOS에서 받아 확인합니다.
   ```sh
   git tag v1.0.0-rc.1 && git push origin v1.0.0-rc.1
   ```
3. 통과하면 `test` → `main` 으로 병합합니다(브랜치 보호 시 PR 경로 — 아래 참고, `--pr` 로 강제 가능).
4. `main`에서 접미사 없는 정식 태그를 push → 정식 릴리즈됩니다.

1–2단계(테스트 프리릴리즈)는 `scripts/release-test.sh`, 3–4단계(정식 릴리즈)는 `scripts/release-main.sh` 로 자동화돼 있습니다(둘 다 `--dry-run` 으로 계획만 확인 가능). `release-test.sh` 는 현재 변경을 `test` 에 커밋·push 한 뒤 다음 `vX.Y.Z-rc.N` 을 자동 증가시켜 태그하고, **태그 push 후 릴리즈 런을 끝까지 감시해 실패하면 rc 태그를 자동 회수**합니다 — 다음 시도가 같은 번호를 재사용하므로 고아 태그가 남지 않습니다(`--no-wait` 로 감시 생략, 타임아웃·Ctrl-C 는 회수하지 않음). `release-main.sh` 는 `test` → `main` 병합·push 후 접미사를 뗀 정식 `vX.Y.Z` 를 태그하고 같은 방식으로 런을 감시하지만, **정식 태그는 실패해도 회수하지 않고** 재실행(`gh run rerun`)을 안내합니다(버전 생략 시 가장 높은 `-rc` 태그에서 도출). 릴리즈 본문은 conventional commit 커밋 메시지를 분류해 자동 생성합니다(`scripts/release-notes.sh`). 정식 태그가 게시되면 `release.yml` 이 그 버전의 `-rc` 프리릴리즈/태그를 **자동으로 정리**합니다(`scripts/prune-rc.sh` — 정식 없이 버려진 버전 라인은 로컬에서 `bash scripts/prune-rc.sh --dry-run vX.Y.Z` 로 확인 후 직접 정리할 수 있습니다). 런 감시·회수·정리에는 [GitHub CLI(`gh`)](https://cli.github.com) 로그인이 필요하며, 없으면 릴리즈 스크립트는 해당 단계만 건너뜁니다(push·태그까지는 종전 동작과 동일. 단독 실행하는 `verify-release.sh`·`prune-rc.sh` 는 gh 필수).

**main 브랜치 보호(선택):** PR 필수·CI 통과 필수 보호를 켜려면 아래 ruleset 을 한 번 적용합니다. 이후 `release-main.sh` 는 직접 push 가 거부되면 자동으로 PR 경로를 사용합니다(필수 체크 이름 3종은 `ci.yml` 의 잡 이름과 일치해야 합니다):

```sh
gh api -X POST repos/vitus9988/psdns/rulesets --input - <<'JSON'
{ "name": "protect-main", "target": "branch", "enforcement": "active",
  "conditions": { "ref_name": { "include": ["refs/heads/main"], "exclude": [] } },
  "rules": [
    { "type": "pull_request", "parameters": { "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": false, "require_code_owner_review": false,
        "require_last_push_approval": false, "required_review_thread_resolution": false } },
    { "type": "required_status_checks", "parameters": { "strict_required_status_checks_policy": false,
        "required_status_checks": [ { "context": "lint + test" },
          { "context": "gui vet + test (macos-latest)" }, { "context": "gui vet + test (windows-latest)" } ] } },
    { "type": "deletion" }, { "type": "non_fast_forward" } ] }
JSON
```

`-`가 들어간 태그(`v1.0.0-rc.1`)는 자동으로 GitHub 프리릴리즈로 게시되며, `/releases/latest`가 프리릴리즈를 제외하므로(그리고 안정 빌드는 프리릴리즈로 자동 업데이트되지 않으므로) **기존 사용자에게 자동 배포되지 않습니다.** 접미사 없는 `vX.Y.Z`만 모든 사용자에게 자동 업데이트로 제안됩니다.

## macOS 서명·공증 (선택)

macOS `psdns.app`의 Gatekeeper 경고를 없애려면 Apple Developer Program(연 $99) 계정으로 Developer ID 서명·공증을 적용합니다. 다음 GitHub Secrets를 설정하면 릴리즈 워크플로가 자동으로 서명·공증·스테이플합니다(설정 전에는 미서명으로 빌드되어 릴리즈가 그대로 동작):

| Secret | 설명 |
|---|---|
| `MACOS_CERT_P12_BASE64` | Developer ID Application 인증서(.p12)를 base64로 인코딩한 값 |
| `MACOS_CERT_PASSWORD` | 위 .p12 내보내기 암호 |
| `MACOS_SIGN_IDENTITY` | 서명 식별자, 예: `Developer ID Application: MinSeong Kim (TEAMID)` |
| `MACOS_NOTARY_APPLE_ID` | 공증용 Apple ID 이메일 |
| `MACOS_NOTARY_PASSWORD` | 공증용 앱 암호(app-specific password) |
| `MACOS_NOTARY_TEAM_ID` | Apple Developer 팀 ID |

서명만 하고 공증 자격증명(`MACOS_NOTARY_*`)이 없으면 서명까지만 적용됩니다(이 경우 Gatekeeper 경고는 남습니다). 번들 식별자는 `io.github.vitus9988.psdns`이며 `MACOS_BUNDLE_ID` 환경변수로 바꿀 수 있습니다.
