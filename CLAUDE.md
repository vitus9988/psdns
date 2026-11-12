# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

psdns — 개인용 크로스플랫폼 DNS·SNI 차단 우회 도구(CLI + Wails GUI). 코드·문서로 알 수 있는 내용은 여기 적지 않는다.

## 찾아갈 곳

- 사용법·플래그·빌드·구조·릴리즈 절차: `README.md`
- 모듈 의존·요청 흐름: `docs/architecture.md`
- 검사·테스트: `.github/workflows/ci.yml` 의 단계를 로컬에서 그대로 실행(GUI cgo 패키지 `cmd/psdns-gui`·`internal/gui` 는 분리)
- 릴리즈: `/release-test`(rc 프리릴리즈) · `/release-main`(정식) 스킬
- 설계 결정의 이유: 해당 코드 주석

## 프로젝트 규칙

- 우회 기법은 유저스페이스 소켓 write 만 쓴다. raw socket·커널·드라이버·방화벽 후킹은 도입 금지(관리자 권한 불필요 + 3개 OS 동일 동작이 핵심 요구).
- UI 셸(Wails/cgo)과 OS 사용자수준 설정 변경(시스템 프록시)은 우회 기법이 아니므로 위 금지와 무관하다.
- SNI 우회가 특정 회선에서 안 통하는 것은 버그가 아니다. 새 우회 전략은 `none|split|tls-record` 와 병렬 옵션으로 추가하고 기본 동작을 바꾸지 않는다.
- 코드 주석이 "제거 금지"·"되돌리지 말 것"이라 적은 처리는 그 이유를 읽기 전에 지우거나 단순화하지 않는다(대부분 인터넷 단절·포트 충돌 재발 방지용).

## 작업 시 주의

- `go.mod` 의 Go 버전을 바꾸면 README §빌드의 Go 요구를 맞춘다.
- Linux apt 의존성은 `release.yml` 과 README 를 수동 동기화한다(Wails 버전 3자는 `release.yml` `checks` 잡이 자동 대조).

## graphify

이 저장소는 graphify 코드 지식 그래프를 쓴다. 결과물은 `graphify-out/`(gitignore).

- 아키텍처/코드 질문 전 `graphify-out/GRAPH_REPORT.md` 의 god node·community 구조를 확인.
- pre-commit 훅이 커밋 시 `graphify update .` 로 자동 갱신(AST-only, 토큰 0).
- 대규모 리팩터(코드 삭제 다수) 후 수동: `graphify update . --force`.
