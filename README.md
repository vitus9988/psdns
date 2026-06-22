# psdns

**personal dns** — 유료 VPN 구독이나 원격 서버 없이, 개인 PC에서 동작하는 크로스플랫폼 차단 우회 도구.

- **DNS 차단 우회** — 모든 질의를 DNS-over-HTTPS(DoH)로 직접 해석해 통신사 DNS 위조(차단 페이지로의 응답 변조)를 무력화합니다.
- **HTTPS(SNI) 차단 우회** — 로컬 프록시가 업스트림으로 보내는 TLS ClientHello를 여러 조각으로 나눠 전송해, 통신사 DPI가 평문 SNI를 파싱하지 못하게 합니다.

순수 유저스페이스(일반 소켓 write)만 사용하므로 Windows / macOS / Linux에서 동일하게 동작하고, 관리자 권한도 필요 없습니다(시스템 DNS 모드 제외).

**두 가지 형태로 제공됩니다.** 터미널용 CLI(`psdns`)와, 처음 쓰는 사람도 큰 버튼 하나로 켜고 끌 수 있는 데스크톱 GUI(`psdns-gui`)입니다. 두 실행파일은 같은 릴리즈 아카이브에 함께 들어 있습니다.

> ⚠️ SNI 우회 효과는 통신사 DPI 구현에 따라 달라지며 **100% 보장되지 않습니다.** 일부 사이트/회선에서는 통하지 않을 수 있습니다.

## 동작 원리

| 차단 방식 | 통신사 동작 | psdns 대응 |
|---|---|---|
| DNS 차단 | 차단 도메인 질의에 위조 응답(warning.or.kr 등) 반환 | DoH로 직접 해석 → 통신사 DNS를 거치지 않음 |
| SNI 기반 HTTPS 차단 | TLS ClientHello의 평문 SNI를 DPI로 검사 후 TCP RST 주입 | ClientHello를 여러 TCP 세그먼트/TLS 레코드로 분할 → DPI의 SNI 파싱 실패 |

프록시 모드는 클라이언트↔서버 TCP를 종단 분리하므로, 업스트림으로 나가는 세그먼트 경계를 완전히 제어할 수 있습니다. 이름 해석도 프록시 내부에서 DoH로 처리하므로 DNS·SNI 차단을 한 번에 우회합니다.

## 설치 (릴리즈 바이너리)

빌드 없이 바로 쓰려면 [GitHub Releases](https://github.com/vitus9988/psdns/releases)에서 OS/아키텍처에 맞는 아카이브를 받아 **압축만 풀면 됩니다.** 각 아카이브에는 CLI(`psdns`)와 GUI(`psdns-gui`)가 함께 들어 있습니다. CLI는 의존성 없는 단일 정적 실행파일이고, GUI는 OS에 내장된 웹뷰(Windows = WebView2, macOS = WebKit, Linux = WebKitGTK)를 사용합니다.

| OS | 아카이브 | GUI 포함 |
|---|---|---|
| Windows | `psdns_<버전>_windows_<amd64\|arm64>.zip` | amd64만 |
| macOS | `psdns_<버전>_darwin_<amd64\|arm64>.tar.gz` | 둘 다 (universal) |
| Linux | `psdns_<버전>_linux_<amd64\|arm64>.tar.gz` | amd64만 |

> ARM(Windows/Linux arm64) 아카이브는 현재 CLI만 포함합니다.

```sh
# 예: macOS Apple Silicon
tar -xzf psdns_v1.0.0_darwin_arm64.tar.gz
cd psdns_v1.0.0_darwin_arm64
./psdns version
./psdns proxy
```

**GUI로 쓰려면** 압축을 푼 뒤 `psdns-gui`(macOS는 `psdns.app`)를 실행하세요. 창이 열리면 **보호 시작하기** 버튼 하나면 됩니다 — 기본적으로 이 컴퓨터의 시스템 웹 프록시(http·https)를 psdns로 **자동 설정**하므로 브라우저 프록시를 직접 켤 필요가 없고, 보호를 중지하거나 앱을 종료하면 **원래대로 되돌립니다.** (자동 설정을 끄려면 화면의 **시스템 프록시 자동 설정** 토글을 끄고, 표시되는 프록시 주소를 복사해 직접 넣으면 됩니다. Firefox는 OS 프록시 대신 자체 설정을 쓰므로 영향을 받지 않습니다.) 창의 닫기(X) 버튼을 누르면 앱이 종료되지 않고 트레이(macOS는 메뉴바) 아이콘으로 최소화되어 보호가 계속 동작합니다 — 완전히 끄려면 트레이 아이콘을 우클릭해 **종료하기**를 선택하세요(아이콘을 클릭하면 창이 다시 열립니다). 새 버전이 나오면 앱이 알려 주고 버튼 하나로 업데이트합니다(아래 [자동 업데이트](#자동-업데이트) 참고).

> **macOS에서 "확인할 수 없습니다" 경고가 뜨면** — 현재 릴리즈는 Apple 공증(notarization) 전이라 Gatekeeper가 실행을 막을 수 있습니다(*"Apple은 'psdns'에 … 악성 코드가 없음을 확인할 수 없습니다"*). 아래 중 하나로 한 번만 허용하면 됩니다:
> - **`psdns gui` (가장 간단)**: 압축 푼 폴더에서 `./psdns gui` 를 실행하면 같은 폴더의 `psdns.app` 격리를 자동으로 벗기고 GUI를 띄웁니다. 이후에는 앱을 더블클릭해도 바로 열립니다. (터미널에서 실행하는 CLI는 Gatekeeper 검사를 받지 않으므로 격리된 상태에서도 동작합니다.)
> - **터미널(수동)**: `xattr -dr com.apple.quarantine psdns.app` 실행 후 다시 열기 (CLI도 쓰면 `xattr -dr com.apple.quarantine psdns`)
> - **Finder**: `psdns.app`을 우클릭 → **열기** → 다시 **열기**
> - **시스템 설정**: 한 번 실행을 시도한 뒤 **개인정보 보호 및 보안** → **그래도 열기**
>
> 정식 서명·공증이 적용된 빌드부터는 이 단계가 필요 없습니다.

> **Windows에서 프록시가 안 켜질 때** — 기본 포트(8080·1080)를 다른 프로그램이 쓰고 있거나, Hyper-V·WSL2·Docker Desktop이 예약한 포트 범위(`netsh int ipv4 show excludedportrange protocol=tcp`로 확인)에 걸리면, psdns-gui가 **자동으로 다른 빈 포트를 골라** 켜고 창에 실제 주소를 표시합니다 — 그 주소를 복사해 쓰면 됩니다. 8080 자체를 꼭 써야 한다면 그 포트를 점유한 프로그램을 끄거나 위 예약 범위를 조정하세요.

무결성 검증: `shasum -a 256 -c psdns_<버전>_checksums.txt` (Windows는 `CertUtil -hashfile <파일> SHA256`).

## 빌드

Go 1.24 이상이 필요합니다. 의존성은 `go.mod`/`go.sum` 에 고정돼 있어, 클론 후 빌드하면 Go 가 알아서 받습니다.

```sh
go build ./cmd/psdns        # 현재 OS용 바이너리

# 크로스 컴파일
GOOS=windows GOARCH=amd64 go build -o psdns.exe ./cmd/psdns
GOOS=darwin  GOARCH=arm64 go build -o psdns      ./cmd/psdns
GOOS=linux   GOARCH=amd64 go build -o psdns      ./cmd/psdns
```

### GUI 빌드 (선택)

GUI(`psdns-gui`)는 [Wails](https://wails.io)로 빌드하며, CLI와 달리 **OS별 네이티브 툴체인(CGO)**이 필요해 크로스 컴파일은 불가합니다. [Wails 사전 요구사항](https://wails.io/docs/gettingstarted/installation)을 갖춘 뒤:

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0
cd cmd/psdns-gui && wails build      # build/bin/ 에 산출
# Linux는 libgtk-3-dev, libwebkit2gtk-4.1-dev, libayatana-appindicator3-dev(트레이) 설치 후 `wails build -tags webkit2_41`
```

프런트엔드는 빌드 단계가 없는 정적 HTML/CSS/JS라 Node가 필요 없습니다.

## 사용법

```
psdns resolve [flags]   로컬 DoH 리졸버 실행 (OS DNS를 이 주소로 지정)
psdns proxy   [flags]   로컬 HTTP CONNECT + SOCKS5 프록시 실행 (브라우저를 이 주소로 지정)
psdns run     [flags]   리졸버와 프록시를 동시에 실행
psdns update  [flags]   최신 릴리즈를 받아 자신을 교체 (-check 는 새 버전 확인만)
psdns gui               (macOS) psdns.app 격리(quarantine) 해제 후 실행 — Gatekeeper 경고 회피
```

> 터미널이 익숙하지 않다면 GUI(`psdns-gui`, macOS는 `psdns.app`)를 실행하세요. 아래 모든 기능(모드 선택·분할 전략·고급 설정)을 버튼과 토글로 제어하고, 프록시 주소 복사·자동 업데이트까지 한 화면에서 처리합니다.

### 권장: 프록시 모드 (자기완결적, 권한 불필요)

```sh
psdns proxy
# HTTP CONNECT  127.0.0.1:8080
# SOCKS5        127.0.0.1:1080
```

브라우저/시스템 프록시를 `127.0.0.1:8080`(HTTP) 또는 `127.0.0.1:1080`(SOCKS5)로 지정하면 됩니다. 시스템 DNS를 바꿀 필요가 없습니다.

### DNS 리졸버 모드 (시스템 전역 DNS 우회)

```sh
sudo psdns resolve                       # 표준 53번 포트는 권한 필요
psdns resolve -listen 127.0.0.1:5353     # 비특권 포트
```

그 후 OS의 DNS 서버를 `127.0.0.1`로 설정합니다. (DNS 차단만 우회되며, SNI 차단은 우회되지 않습니다.)

### 공통 플래그

| 플래그 | 기본값 | 설명 |
|---|---|---|
| `-doh URL` | `https://1.1.1.1/dns-query` | 업스트림 DoH 엔드포인트 |
| `-bootstrap IP` | (없음) | DoH 호스트를 해석할 IP[:port] (시스템 DNS 우회) |
| `-frag STRATEGY` | `split` | ClientHello 분할 전략: `none` / `split` / `tls-record` |
| `-frag-delay D` | `0` | 조각 사이 지연 (예: `10ms`) |
| `-timeout D` | `10s` | dial/질의 타임아웃 |

- `-http ADDR` / `-socks ADDR` : 프록시 listen 주소 (`proxy`, `run`)
- `-listen ADDR` : DNS listen 주소 (`resolve`)
- `-dns ADDR` : DNS listen 주소 (`run`)

### 분할 전략

- **`split`** (기본): ClientHello를 여러 TCP 세그먼트로 나눠 전송. SNI 호스트명 위치를 찾아 그 내부에서 자르고, 파싱 실패 시 레코드 헤더 직후에서 자릅니다.
- **`tls-record`**: ClientHello 핸드셰이크를 여러 TLS 레코드로 재구성(RFC 8446 §5.1 허용). 첫 레코드만 검사하는 DPI를 회피합니다.
- **`none`**: 분할 없음 (DNS 차단만 우회, 비교/디버그용).

기본 DoH 엔드포인트는 IP 리터럴 호스트(`1.1.1.1`)라서 DoH 연결 자체에는 SNI가 실리지 않고 DNS 부트스트랩도 필요 없습니다.

## 한계 및 고지

- SNI fragmentation 효과는 통신사 DPI 구현에 의존하므로 **모든 사이트/회선에서 보장되지 않습니다.** 안 통하는 경우 ECH(대상 사이트가 지원할 때) 또는 Tor 등 풀터널 방식이 대안입니다. 이 방식이 더 강하게 차단될 때 psdns가 철학(순수 유저스페이스·무설치·경량)을 지키며 추가로 적용할 수 있는 우회 전략 로드맵은 [`docs/bypass-roadmap.md`](docs/bypass-roadmap.md)에 정리돼 있습니다.
- 프록시 모드는 해당 프록시를 사용하도록 설정한 앱(브라우저 등)에만 적용됩니다. GUI의 **시스템 프록시 자동 설정**(기본 켜짐)은 OS 전역 웹 프록시를 psdns로 지정해 OS 프록시를 따르는 앱 전반에 적용되지만, Firefox 등 자체 프록시 설정을 쓰는 앱은 제외됩니다. **macOS에서는 시스템 프록시 변경에 관리자 권한이 필요할 수 있어** 자동 설정이 거부되면 주소를 복사해 직접 넣어야 할 수 있습니다(Windows·Linux는 사용자 권한으로 동작). 비정상 종료로 설정이 남아도 다음 실행 때 자동으로 정리됩니다.
- GUI(`psdns-gui`)는 OS 내장 웹뷰를 사용합니다 — Windows는 WebView2(Win10+ 기본 탑재), macOS는 시스템 WebKit, Linux는 WebKitGTK(`libwebkit2gtk`) 런타임이 필요합니다(Linux는 트레이 표시에 `libayatana-appindicator3`도 필요). CLI(`psdns`)는 의존성 없는 단일 정적 바이너리입니다.
- VPN/우회 기술 자체는 한국에서 합법입니다. 본 도구는 차단 메커니즘의 이해·연구 목적으로 제공되며, **접근 대상 콘텐츠의 적법성과 관련 정책 준수 책임은 사용자에게 있습니다.**

## 자동 업데이트

GUI(`psdns-gui`)는 시작할 때 [GitHub Releases](https://github.com/vitus9988/psdns/releases)에서 최신 버전을 확인하고, 새 버전이 있으면 배너로 알립니다. **업데이트** 버튼을 누르면 현재 OS/아키텍처 아카이브를 내려받아 published `checksums.txt`로 SHA-256 검증한 뒤, 실행 파일을 원자적으로 교체하고 재시작합니다(Windows의 실행 중 교체 포함). 검증에 실패하면 교체하지 않고 릴리즈 페이지 링크를 안내합니다. 개발 빌드(`dev`)나 정식 태그가 아닌 빌드는 자동 적용 대상에서 제외됩니다.

CLI(`psdns`)도 같은 검증 로직을 공유합니다. `psdns update`를 실행하면 현재 OS/아키텍처 아카이브를 내려받아 `checksums.txt`로 SHA-256 검증한 뒤 실행 파일을 교체합니다(`psdns update -check`는 새 버전 존재 여부만 확인하고 교체하지 않음). `proxy`·`run`으로 장시간 실행 중이면 시작 시 새 버전이 있을 때 로그로 한 줄 알려 주며(자동 적용은 하지 않음), 교체 후에는 프로세스를 다시 실행해야 새 버전이 적용됩니다. GUI와 동일하게 개발 빌드(`dev`)는 자동 업데이트 대상에서 제외됩니다.

## 릴리즈 발행

버전 태그를 push하면 GitHub Actions(`.github/workflows/release.yml`)가 **OS별 러너**에서 빌드해 6개 아카이브를 Release로 자동 게시합니다. mainstream 4개 타깃(macOS amd64·arm64, Windows amd64, Linux amd64)에는 CLI와 GUI가 함께, ARM 2개 타깃(Windows/Linux arm64)에는 CLI만 담깁니다. (CLI는 `CGO_ENABLED=0`으로 어디서나 크로스 컴파일되지만, Wails GUI는 OS별 네이티브 빌드가 필요해 매트릭스를 씁니다.)

```sh
git tag v1.0.0
git push origin v1.0.0
```

로컬에서 만들려면 `scripts/build-release.sh [버전]` → `dist/`. CLI 6타깃은 항상 빌드되고, GUI는 (Wails CLI가 있으면) **빌드를 실행한 호스트 OS용 아카이브에만** 포함됩니다. 전체 플랫폼 GUI는 CI에서 생성됩니다.

### 테스트 채널 (프리릴리즈)

서명·포트·패키징 같은 산출물 전용 문제는 실제 릴리즈에서만 드러나므로, 정식 배포 전에 **프리릴리즈로 실물을 검증**합니다.

1. 수정사항을 `test` 브랜치에 push → `ci.yml`(gofmt·vet·test)이 돕니다.
2. 프리릴리즈 태그를 push → `release.yml`이 6개 아카이브를 **프리릴리즈**로 게시합니다. 실제 Windows/macOS에서 받아 검증합니다.
   ```sh
   git tag v1.0.0-rc.1 && git push origin v1.0.0-rc.1
   ```
3. 통과하면 `test` → `main` 으로 PR·머지합니다.
4. `main`에서 접미사 없는 정식 태그를 push → 정식 릴리즈됩니다.

1–2단계(테스트 프리릴리즈)는 `scripts/release-test.sh`, 3–4단계(정식 릴리즈)는 `scripts/release-main.sh` 로 자동화돼 있습니다(둘 다 `--dry-run` 으로 계획만 확인 가능). `release-test.sh` 는 현재 변경을 `test` 에 커밋·push 한 뒤 다음 `vX.Y.Z-rc.N` 을 자동 증가시켜 태그하고, `release-main.sh` 는 `test` → `main` 병합·push 후 접미사를 뗀 정식 `vX.Y.Z` 를 태그합니다(버전 생략 시 가장 높은 `-rc` 태그에서 도출).

`-`가 들어간 태그(`v1.0.0-rc.1`)는 자동으로 GitHub 프리릴리즈로 게시되며, `/releases/latest`가 프리릴리즈를 제외하므로(그리고 안정 빌드는 프리릴리즈로 자동 업데이트되지 않으므로) **기존 사용자에게 자동 배포되지 않습니다.** 접미사 없는 `vX.Y.Z`만 모든 사용자에게 자동 업데이트로 제안됩니다.

### macOS 서명·공증 (선택)

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

## 구조

```
cmd/psdns           CLI 진입점 (resolve / proxy / run / version)
cmd/psdns-gui       GUI 진입점 (Wails 데스크톱 앱 + 정적 프런트엔드)
internal/config     런타임 설정
internal/doh        DoH 클라이언트 (RFC 8484)
internal/resolver   host -> IP 해석 (TTL 캐시)
internal/dnssrv     로컬 DNS 서버 (UDP/TCP -> DoH)
internal/proxy      HTTP CONNECT + SOCKS5 프록시 + 릴레이
internal/frag       ClientHello 파싱 및 분할 전략
internal/supervisor 서버 start/stop 오케스트레이션 (GUI가 구동)
internal/gui        Wails 바인딩 (App 메서드·트레이/닫기 동작, 프런트엔드가 호출)
internal/uiconfig   GUI 설정 DTO ↔ config 변환·검증
internal/selfupdate GitHub Releases 자동 업데이트 (확인·검증·교체)
docs/               설계 문서 (우회 전략 로드맵 등)
scripts/            크로스 빌드·패키징 스크립트
.github/workflows   릴리즈 자동화 (태그 push, OS별 매트릭스)
```

## 라이선스

MIT © 2026 MinSeong Kim
