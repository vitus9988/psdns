# psdns

**personal dns** — 유료 VPN이나 원격 서버 없이 내 PC에서 돌아가는 차단 우회 도구입니다. Windows · macOS · Linux를 지원합니다.

- **DNS 차단 우회** — 통신사 DNS 대신 DNS-over-HTTPS(DoH)로 주소를 찾아, 차단 페이지(warning.or.kr 등)로 보내지는 것을 막습니다.
- **HTTPS(SNI) 차단 우회** — 접속할 사이트 이름이 담긴 첫 패킷을 잘게 나눠 보내 통신사가 읽지 못하게 합니다.

관리자 권한 없이 동작합니다(시스템 DNS 모드 제외). 터미널용 CLI(`psdns`)와 버튼 하나로 켜고 끄는 데스크톱 앱(`psdns-gui`)이 한 압축 파일에 들어 있습니다.

| | |
|---|---|
| ✅ 한다 | 통신사 **DNS 위조**와 **SNI 기반 HTTPS 차단** 우회 |
| ❌ 아니다 | **VPN이 아님** — 트래픽을 터널링하지 않고 IP·위치를 숨기지 않음 |
| ❌ 못 한다 | 통신사가 **IP 주소로** 직접 막은 차단 |
| ⚠️ 보장 안 됨 | SNI 우회는 통신사 장비·정책에 따라 안 통할 수 있음 |

## 설치

[GitHub Releases](https://github.com/vitus9988/psdns/releases)에서 OS에 맞는 파일을 받아 **압축만 풀면 됩니다.**

| OS | 파일 | GUI 포함 |
|---|---|---|
| Windows | `psdns_<버전>_windows_<amd64\|arm64>.zip` | amd64만 |
| macOS | `psdns_<버전>_darwin_<amd64\|arm64>.tar.gz` | 둘 다 |
| Linux | `psdns_<버전>_linux_<amd64\|arm64>.tar.gz` | amd64만 |

Linux GUI는 WebKitGTK와 트레이용 `libayatana-appindicator3`가 필요합니다. 파일 검증(선택): `shasum -a 256 -c psdns_<버전>_checksums.txt` (Windows는 `CertUtil -hashfile <파일> SHA256`).

> **macOS에서 "확인할 수 없습니다" 경고가 뜨면** (아직 Apple 공증 전이라 뜹니다) 아래 중 하나로 한 번만 허용하세요.
> - 압축 푼 폴더에서 `./psdns gui` 실행 — 가장 간단하고, 이후에는 더블클릭으로 열립니다.
> - 터미널에서 `xattr -dr com.apple.quarantine psdns.app`
> - Finder에서 `psdns.app` 우클릭 → **열기** → 다시 **열기**

## GUI 사용법

`psdns-gui`(macOS는 `psdns.app`)를 실행하고 **보호 시작하기**를 누르면 됩니다.

- 컴퓨터의 시스템 프록시를 psdns로 자동 설정하고, 보호를 끄거나 앱을 종료하면 원래대로 되돌립니다. 자동 설정을 원하지 않으면 **시스템 프록시 자동 설정** 토글을 끄고 화면의 주소를 직접 넣으세요.
- Firefox는 프록시 설정이 기본값('시스템 프록시 설정 사용')이면 함께 적용되고, 직접 지정했다면 그 설정을 따릅니다.
- **psdns를 켰을 때만 안 열리는 사이트**는 고급 설정의 **프록시 예외 사이트**에 적으세요(예: `mybank.co.kr`). 그 사이트와 하위 도메인은 psdns를 거치지 않습니다.
- 창을 닫아도(X) 트레이(macOS는 메뉴바)에서 계속 동작합니다. 완전히 끄려면 트레이 아이콘 우클릭 → **종료하기**.
- 기본 포트(8080·1080)가 사용 중이면 다른 빈 포트로 켜고 창에 실제 주소를 보여 줍니다.
- 새 버전이 나오면 앱이 알려 주고, 버튼 하나로 업데이트합니다.

## CLI 사용법

```
psdns proxy     로컬 HTTP·SOCKS5 프록시 실행 (권장)
psdns resolve   로컬 DNS 서버 실행 (DNS 차단만 우회)
psdns run       둘 다 실행
psdns update    최신 버전으로 업데이트 (-check 는 확인만)
psdns gui       (macOS) 경고 해제 후 GUI 실행
psdns version   버전 출력
```

**프록시 모드(권장):** `psdns proxy` 를 실행하고 브라우저나 시스템 프록시를 `127.0.0.1:8080`(HTTP) 또는 `127.0.0.1:1080`(SOCKS5)로 지정합니다.

**DNS 서버 모드:** `sudo psdns resolve`(53번 포트는 관리자 권한 필요, 아니면 `-listen 127.0.0.1:5353`) 후 OS DNS 서버를 `127.0.0.1`로 바꿉니다. SNI 차단은 우회되지 않습니다.

> ⚠️ DNS 서버 모드를 쓰다 psdns를 끄면 **DNS 설정을 직접 원래대로(자동) 되돌리세요.** 그대로 두면 인터넷이 안 됩니다.

### 플래그

| 플래그 | 기본값 | 설명 |
|---|---|---|
| `-doh URL` | `https://1.1.1.1/dns-query` | 사용할 DoH 서버 |
| `-doh-fallbacks URLS` | (없음) | 기본 DoH가 막히거나 느릴 때 쓸 예비 서버(콤마 구분), 예: `https://8.8.8.8/dns-query,https://9.9.9.9/dns-query` |
| `-doh-hedge-delay D` | `250ms` | 예비 DoH 서버로 넘어가기 전 대기 시간 |
| `-bootstrap IP` | (없음) | DoH 서버 주소를 이름 대신 IP[:port]로 지정 |
| `-frag STRATEGY` | `split` | 패킷 분할 방식: `split`(기본) / `tls-record`(split이 안 통할 때) / `none`(분할 안 함) |
| `-frag-delay D` | `0` | 조각 사이 지연 (예: `10ms`) |
| `-timeout D` | `10s` | 연결·질의 대기 시간 |
| `-http` / `-socks ADDR` | `127.0.0.1:8080` / `:1080` | 프록시 주소 (`proxy`, `run`) |
| `-listen` / `-dns ADDR` | `127.0.0.1:53` | DNS 주소 (`resolve` / `run`) |

## 알아 둘 점

- psdns는 프록시를 쓰도록 설정된 앱(브라우저 등)에만 적용됩니다. macOS는 시스템 프록시 변경에 관리자 권한이 필요할 수 있어, 거부되면 주소를 직접 넣어야 합니다. Linux 자동 설정은 GNOME만 지원합니다.
- 다른 로컬 프록시(예: AdGuard)가 시스템 프록시로 동작 중이면 덮어쓰지 않고 자동 설정을 건너뜁니다.
- **DNS 광고 차단(AdGuard 등)과 함께 쓸 때:** psdns가 주소를 직접 찾아 광고 차단이 풀릴 수 있습니다. `-doh https://dns.adguard-dns.com/dns-query`(GUI는 고급 설정의 DoH 주소)처럼 광고 차단 DoH를 지정하면 차단 우회와 광고 차단이 함께 됩니다.
- SNI 우회가 안 통하는 사이트·회선에서는 Tor 같은 풀터널 방식이 대안입니다.
- VPN/우회 기술 자체는 한국에서 합법입니다. 본 도구는 차단 메커니즘의 이해·연구 목적으로 제공되며, **접근 대상 콘텐츠의 적법성과 관련 정책 준수 책임은 사용자에게 있습니다.**

## 빌드

Go 1.24 이상이 필요합니다.

```sh
go build ./cmd/psdns

# GUI (Wails, OS별 네이티브 빌드만 가능)
go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0
cd cmd/psdns-gui && wails build      # build/bin/ 에 산출
# Linux는 libgtk-3-dev, libwebkit2gtk-4.1-dev, libayatana-appindicator3-dev(트레이) 설치 후 `wails build -tags webkit2_41`
```

GUI 빌드 전 [Wails 사전 요구사항](https://wails.io/docs/gettingstarted/installation)을 갖추세요. 개발 문서: [구조](docs/architecture.md) · [릴리즈 절차](docs/release.md) · [측정](docs/measurements.md) · [우회 전략 로드맵](docs/bypass-roadmap.md)

## 라이선스

MIT © 2026 MinSeong Kim
