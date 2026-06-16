# psdns

개인용 크로스플랫폼 차단 우회 도구. 사용법·플래그·빌드·구조는 모두 @README.md 에 있다 (중복 금지).

## 코드/grep 로 알 수 없는 설계 결정 (Why)

- **유저스페이스 전용은 의도된 제약이다.** 분할은 일반 소켓 write 로만 구현한다. raw socket·커널 모듈·드라이버·OS 방화벽 후킹 등 권한이 필요한 방식은 "관리자 권한 불필요 + 3개 OS 동일 동작" 이라는 핵심 요구를 깨므로 도입 금지. (DNS 리졸버의 :53 바인딩만 예외적으로 권한 필요.)
- **SNI 우회 비보장은 버그가 아니다.** 효과는 통신사 DPI 구현에 종속된다. 특정 회선에서 안 통하는 것을 "고쳐야 할 결함" 으로 다루지 말 것. 새 우회 전략은 기존 `none|split|tls-record` 와 병렬 옵션으로 추가하고 기본 동작을 바꾸지 않는다.
- 기본 DoH 가 IP 리터럴 호스트(`1.1.1.1`)인 이유: DoH 연결 자체에 SNI 가 실리지 않고 부트스트랩 DNS 도 불필요. 기본값을 도메인 엔드포인트로 바꾸면 이 속성이 깨진다.
- **GUI(Wails)는 UI 셸 한정 예외다.** `psdns-gui` 는 OS 네이티브 웹뷰를 써서 CGO 필요·OS별 네이티브 빌드·Linux WebKitGTK 런타임 의존이 생긴다. 이는 UI 표면에만 해당하며 차단 우회 코어(분할·DoH)는 여전히 순수 유저스페이스 소켓이다 — 위 "유저스페이스 전용" 금지(raw socket·커널)는 *우회 기법*에 대한 것이라 Wails 도입과 무관하다. Wails 의존성은 `cmd/psdns-gui`·`internal/gui` 에서만 import 해 CLI 바이너리·`CGO_ENABLED=0` 크로스컴파일을 오염시키지 않는다. GUI 켜기/끄기 로직은 `internal/supervisor`(Wails 비의존, 단위 테스트됨)에 둔다.
- **자동 업데이트는 checksums 검증이 필수다.** `internal/selfupdate` 는 published `checksums.txt` 의 SHA-256 으로 내려받은 아카이브를 검증한 뒤에만 교체한다. 검증 단계를 빼지 말 것. 실행 중 자기 교체(특히 Windows)는 `minio/selfupdate` 에 위임한다.

## 작업 시 주의

- `go.mod`/`go.sum` 의 버전(go 1.24.0, miekg/dns, 그리고 GUI용 wails/v2·minio/selfupdate·golang.org/x/mod)을 바꾸면 README §빌드 의 Go/Wails 버전 요구도 함께 맞춰야 한다. CI(`release.yml`)의 `WAILS_VERSION` 과 README·`go install ...wails@vX` 의 버전도 동기화한다.

## Git 브랜치 정책

- **모든 작업과 커밋은 `main` 브랜치에서 진행한다.** 별도의 기능(feature) 브랜치를 만들지 않고 항상 `main` 으로 통일한다. 기본 브랜치라 하더라도 새 브랜치를 파지 말고 `main` 에 직접 커밋한다.
