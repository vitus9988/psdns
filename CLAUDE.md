# psdns

개인용 크로스플랫폼 차단 우회 도구. 사용법·플래그·빌드·구조는 모두 @README.md 에 있다 (중복 금지).

## 코드/grep 로 알 수 없는 설계 결정 (Why)

- **유저스페이스 전용은 의도된 제약이다.** 분할은 일반 소켓 write 로만 구현한다. raw socket·커널 모듈·드라이버·OS 방화벽 후킹 등 권한이 필요한 방식은 "관리자 권한 불필요 + 3개 OS 동일 동작" 이라는 핵심 요구를 깨므로 도입 금지. (DNS 리졸버의 :53 바인딩만 예외적으로 권한 필요.)
- **SNI 우회 비보장은 버그가 아니다.** 효과는 통신사 DPI 구현에 종속된다. 특정 회선에서 안 통하는 것을 "고쳐야 할 결함" 으로 다루지 말 것. 새 우회 전략은 기존 `none|split|tls-record` 와 병렬 옵션으로 추가하고 기본 동작을 바꾸지 않는다.
- 기본 DoH 가 IP 리터럴 호스트(`1.1.1.1`)인 이유: DoH 연결 자체에 SNI 가 실리지 않고 부트스트랩 DNS 도 불필요. 기본값을 도메인 엔드포인트로 바꾸면 이 속성이 깨진다.
- **GUI(Wails)는 UI 셸 한정 예외다.** `psdns-gui` 는 OS 네이티브 웹뷰를 써서 CGO 필요·OS별 네이티브 빌드·Linux WebKitGTK 런타임 의존이 생긴다. 이는 UI 표면에만 해당하며 차단 우회 코어(분할·DoH)는 여전히 순수 유저스페이스 소켓이다 — 위 "유저스페이스 전용" 금지(raw socket·커널)는 *우회 기법*에 대한 것이라 Wails 도입과 무관하다. Wails 의존성은 `cmd/psdns-gui`·`internal/gui` 에서만 import 해 CLI 바이너리·`CGO_ENABLED=0` 크로스컴파일을 오염시키지 않는다. GUI 켜기/끄기 로직은 `internal/supervisor`(Wails 비의존, 단위 테스트됨)에 둔다.
- **자동 업데이트는 checksums 검증이 필수다.** `internal/selfupdate` 는 published `checksums.txt` 의 SHA-256 으로 내려받은 아카이브를 검증한 뒤에만 교체한다. 검증 단계를 빼지 말 것. 실행 중 자기 교체(특히 Windows)는 `minio/selfupdate` 에 위임한다. CLI(`psdns update`)와 GUI 가 이 패키지를 공유하며, 아카이브에서 꺼낼 바이너리만 `Checker.Binary` 로 구분한다(CLI=`psdns`, GUI=기본값 `psdns-gui`). 따라서 CLI 바이너리도 `selfupdate.Version` 주입이 필요하다(`scripts/build-release.sh` 의 `CLI_LDFLAGS` 에 `-X ...selfupdate.Version` 포함). CLI 는 자동 무인 적용을 하지 않고 `psdns update` 수동 실행과 `proxy`/`run` 시작 시 알림만 제공한다.
- **창 닫기(X)는 종료가 아니라 트레이 최소화다.** Wails v2 는 트레이를 네이티브 지원하지 않아(메인테이너가 v2 미지원 명시) `energye/systray` 로 구현한다. Wails 가 메인 루프를 점유하므로 트레이 구동은 OS별로 갈린다(`startTray`): **macOS/Linux 는 `systray.RunWithExternalLoop` 의 `start()/end()` 를 `Startup`/`Shutdown`(정확히는 `stopTray`)에서 구동한다.** **Windows 는 반드시 `systray.Run` 을 `runtime.LockOSThread` 로 고정한 단독 고루틴에서 돌린다 — Win32 는 윈도우 메시지를 그 윈도우를 생성한 스레드의 큐로만 전달하는데, `RunWithExternalLoop` 는 윈도우 생성(호출자 고루틴)과 `GetMessage` 루프(별도 고루틴)를 다른 스레드로 분리해 트레이 클릭이 영영 디스패치되지 않기 때문이다(아이콘은 `Shell_NotifyIcon` 이라 루프 없이도 표시돼 "아이콘만 뜨고 클릭 먹통" 증상이 난다). macOS 에서 `systray.Run` 을 쓰면 `[NSApp run]` 이 이중 호출돼 Wails 와 충돌하므로 Windows 에만 이 분기를 쓴다.** 따라서 Windows 경로는 `trayEnd` 가 nil 이고 `stopTray` 가 `systray.Quit` 으로 정리한다. systray import 는 `internal/gui/tray.go` 에만 둬 CLI(`CGO_ENABLED=0`)로 새지 않게 한다(Linux 트레이는 `libayatana-appindicator3` 런타임 필요). 닫기는 `OnBeforeClose` 가 `true` 를 반환해 종료를 취소하고 `WindowHide` 로 숨긴다. **실제 종료(트레이 '종료하기'·앱 내 종료 버튼)는 `App.quitting` 플래그를 세운 뒤 `runtime.Quit` 을 부른다 — 이 플래그가 없으면 `runtime.Quit` 도 `OnBeforeClose` 를 거쳐 종료가 영원히 막히므로 제거 금지.** macOS 에서 systray 는 NSApplication delegate 를 교체하지만 Wails 의 닫기→종료는 윈도우 delegate(`windowShouldClose`) 경로라 영향받지 않는다(그래서 3개 OS 동일 동작). 트레이 아이콘은 placeholder 이며 `internal/gui/icons/`(`gen.go` 로 생성)에 embed.

## 작업 시 주의

- `go.mod`/`go.sum` 의 버전(go 1.24.0, miekg/dns, 그리고 GUI용 wails/v2·minio/selfupdate·golang.org/x/mod·energye/systray)을 바꾸면 README §빌드 의 Go/Wails 버전 요구도 함께 맞춰야 한다. CI(`release.yml`)의 `WAILS_VERSION` 과 README·`go install ...wails@vX` 의 버전, 그리고 Linux apt 의존성(`libgtk-3-dev`·`libwebkit2gtk-4.1-dev`·트레이용 `libayatana-appindicator3-dev`)도 `release.yml`·README 사이에서 동기화한다. wails/v2 버전 3자(`WAILS_VERSION`·README `wails@vX`·`go.mod`)는 `release.yml` 의 `checks` 잡이 빌드 전에 자동 대조하므로 불일치하면 릴리즈가 실패한다(단 apt 의존성 동기화는 여전히 수동). 같은 `checks` 잡이 `gofmt`·`go vet`(GUI cgo 패키지 제외)도 돌린다.

## Git 브랜치 정책

- **모든 작업과 커밋은 `main` 브랜치에서 진행한다.** 별도의 기능(feature) 브랜치를 만들지 않고 항상 `main` 으로 통일한다. 기본 브랜치라 하더라도 새 브랜치를 파지 말고 `main` 에 직접 커밋한다.
