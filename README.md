# psdns

**personal dns** — 유료 VPN 구독이나 원격 서버 없이, 개인 PC에서 동작하는 크로스플랫폼 차단 우회 도구.

- **DNS 차단 우회** — 모든 질의를 DNS-over-HTTPS(DoH)로 직접 해석해 통신사 DNS 위조(차단 페이지로의 응답 변조)를 무력화합니다.
- **HTTPS(SNI) 차단 우회** — 로컬 프록시가 업스트림으로 보내는 TLS ClientHello를 여러 조각으로 나눠 전송해, 통신사 DPI가 평문 SNI를 파싱하지 못하게 합니다.

순수 유저스페이스(일반 소켓 write)만 사용하므로 Windows / macOS / Linux에서 동일하게 동작하고, 관리자 권한도 필요 없습니다(시스템 DNS 모드 제외).

> ⚠️ SNI 우회 효과는 통신사 DPI 구현에 따라 달라지며 **100% 보장되지 않습니다.** 일부 사이트/회선에서는 통하지 않을 수 있습니다.

## 동작 원리

| 차단 방식 | 통신사 동작 | psdns 대응 |
|---|---|---|
| DNS 차단 | 차단 도메인 질의에 위조 응답(warning.or.kr 등) 반환 | DoH로 직접 해석 → 통신사 DNS를 거치지 않음 |
| SNI 기반 HTTPS 차단 | TLS ClientHello의 평문 SNI를 DPI로 검사 후 TCP RST 주입 | ClientHello를 여러 TCP 세그먼트/TLS 레코드로 분할 → DPI의 SNI 파싱 실패 |

프록시 모드는 클라이언트↔서버 TCP를 종단 분리하므로, 업스트림으로 나가는 세그먼트 경계를 완전히 제어할 수 있습니다. 이름 해석도 프록시 내부에서 DoH로 처리하므로 DNS·SNI 차단을 한 번에 우회합니다.

## 설치 (릴리즈 바이너리)

빌드 없이 바로 쓰려면 [GitHub Releases](https://github.com/vitus9988/psdns/releases)에서 OS/아키텍처에 맞는 아카이브를 받아 **압축만 풀면 됩니다.** 의존성·런타임이 필요 없는 단일 정적 실행파일입니다.

| OS | 아카이브 |
|---|---|
| Windows | `psdns_<버전>_windows_<amd64\|arm64>.zip` |
| macOS | `psdns_<버전>_darwin_<amd64\|arm64>.tar.gz` |
| Linux | `psdns_<버전>_linux_<amd64\|arm64>.tar.gz` |

```sh
# 예: macOS Apple Silicon
tar -xzf psdns_v1.0.0_darwin_arm64.tar.gz
cd psdns_v1.0.0_darwin_arm64
./psdns version
./psdns proxy
```

무결성 검증: `shasum -a 256 -c psdns_<버전>_checksums.txt` (Windows는 `CertUtil -hashfile <파일> SHA256`).

## 빌드

Go 1.24 이상이 필요합니다.

```sh
go get github.com/miekg/dns # 의존성 받기 (go.sum 생성)
go mod tidy
go build ./cmd/psdns        # 현재 OS용 바이너리

# 크로스 컴파일
GOOS=windows GOARCH=amd64 go build -o psdns.exe ./cmd/psdns
GOOS=darwin  GOARCH=arm64 go build -o psdns      ./cmd/psdns
GOOS=linux   GOARCH=amd64 go build -o psdns      ./cmd/psdns
```

## 사용법

```
psdns resolve [flags]   로컬 DoH 리졸버 실행 (OS DNS를 이 주소로 지정)
psdns proxy   [flags]   로컬 HTTP CONNECT + SOCKS5 프록시 실행 (브라우저를 이 주소로 지정)
psdns run     [flags]   리졸버와 프록시를 동시에 실행
```

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

- SNI fragmentation 효과는 통신사 DPI 구현에 의존하므로 **모든 사이트/회선에서 보장되지 않습니다.** 안 통하는 경우 ECH(대상 사이트가 지원할 때) 또는 Tor 등 풀터널 방식이 대안입니다.
- 프록시 모드는 해당 프록시를 사용하도록 설정한 앱(브라우저 등)에만 적용됩니다.
- VPN/우회 기술 자체는 한국에서 합법입니다. 본 도구는 차단 메커니즘의 이해·연구 목적으로 제공되며, **접근 대상 콘텐츠의 적법성과 관련 정책 준수 책임은 사용자에게 있습니다.**

## 릴리즈 발행

버전 태그를 push하면 GitHub Actions(`.github/workflows/release.yml`)가 6개 타깃(Windows/macOS/Linux × amd64/arm64)을 빌드·패키징해 Release로 자동 게시합니다.

```sh
git tag v1.0.0
git push origin v1.0.0
```

로컬에서 동일한 아카이브를 만들려면: `scripts/build-release.sh [버전]` → `dist/`.

## 구조

```
cmd/psdns         CLI 진입점 (resolve / proxy / run / version)
internal/config   런타임 설정
internal/doh      DoH 클라이언트 (RFC 8484)
internal/resolver host -> IP 해석 (TTL 캐시)
internal/dnssrv   로컬 DNS 서버 (UDP/TCP -> DoH)
internal/proxy    HTTP CONNECT + SOCKS5 프록시 + 릴레이
internal/frag     ClientHello 파싱 및 분할 전략
scripts/          크로스 빌드·패키징 스크립트
.github/workflows 릴리즈 자동화 (태그 push)
```

## 라이선스

MIT © 2026 MinSeong Kim
