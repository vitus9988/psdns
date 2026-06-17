# psdns 우회 전략 로드맵

이 문서는 **미래 대비 설계 방향**이다. 지금 구현된 기능 설명이 아니라(그건 [README](../README.md) 참고), 현재의 SNI/DNS 우회가 통신사 DPI 강화로 무력화될 경우 **추가로 적용할 수 있는 기법**과, 그중 무엇이 psdns 의 철학에 맞고 무엇이 맞지 않는지를 정리한다. 차단이 실제로 강화되었을 때 이 문서의 우선순위대로 구현에 착수한다.

## 현재 방식과 한계

SNI 우회는 로컬 프록시(`internal/proxy`의 `relay()`)가 브라우저의 첫 TLS 레코드를 떼어내 `frag.WriteFirst()` 로 **2-way 단순 분할**(TCP 세그먼트 또는 TLS 레코드)하는 것이다. DNS 우회는 DoH 단일 엔드포인트(기본 `1.1.1.1` IP 리터럴)로 직접 해석한다.

DPI 가 분할된 세그먼트를 **재조립(reassembly)** 한 뒤 SNI 를 검사하거나, DoH IP/엔드포인트 자체를 차단하기 시작하면 효과가 약해질 수 있다. 아래는 그 시나리오별 대응이다.

## 1. 대전제 — 모든 판단의 기준이 되는 3가지 철학 제약

새 기법을 채택할지는 **항상 이 세 제약으로 판정한다.** 하나라도 어기면 배제한다.

1. **순수 유저스페이스 일반 소켓 write 만 쓴다.** raw socket·커널 모듈·드라이버·OS 방화벽/패킷 후킹·IP TTL 조작·가짜(fake/decoy) 패킷 주입·TCP 세그먼트 재정렬(out-of-order)처럼 권한이나 패킷 수준 제어가 필요한 기법은 전부 금지. ("관리자 권한 불필요 + 3개 OS 동일 동작"이라는 핵심 요구를 깨기 때문.)
2. **무설치·경량.** 부가 프로그램 설치 없음, 외부 의존성 추가는 최소(특히 `quic-go` 같은 무거운 스택 지양). Windows/macOS/Linux 에서 동일 동작.
3. **기본 동작 불변.** 새 전략은 기존 `none|split|tls-record` 와 **병렬 opt-in** 으로만 추가하고, `config.Default()` 의 기본값은 바꾸지 않는다.

## 2. 유명 DPI 우회 기법 분류 — psdns 에서 무엇이 가능한가

`byedpi`·`zapret`·`GoodbyeDPI` 등 알려진 유저스페이스 도구의 기법을 "**일반 TCP 소켓 write 만으로 되는가**" 기준으로 재분류한 것이다.

| 기법 | 동작 | raw socket / TTL 필요? | psdns |
|---|---|---|---|
| split | 지정 위치에서 페이로드를 여러 TCP 세그먼트로 | 불필요 (순수 write) | **이미 구현** (`split`) |
| tls-record | ClientHello 를 여러 TLS 레코드로 재구성 | 불필요 (순수 write) | **이미 구현** (`tls-record`) |
| multi-split / 분할점 다양화 | N-way 분할 + 자르는 위치를 SNI 시작·중앙·끝 등으로 | 불필요 (순수 write) | **가능** → §3-A |
| 복수 DoH / hedged | 여러 DoH 엔드포인트에 동시/순차 질의 | 불필요 | **가능** → §3-B |
| mod-http (Host 변형) | 평문 HTTP Host 헤더 대소문자 섞기·공백 조정 | 불필요 (앱 레이어) | 조건부 → §4-C1 |
| disorder (역순 전송) | 조각을 역순으로 보냄 | **필요** — 효과를 내려면 두 번째 조각을 **TTL=1** 로 보내 서버 도달을 막아야 함 | **배제** (§5) |
| fake packet | 진짜 데이터 앞에 가짜 패킷 주입 | **필요** — 가짜 패킷은 서버에 도달하면 안 되므로 TTL 제어 + raw socket | **배제** (§5) |

**결정적 사실:** `disorder`·`fake` 가 효과를 내는 본질은 "가짜/역순 조각이 경로상 DPI 는 통과하되 **TTL=1 때문에 목적지 서버에는 도달하지 못하게** 만들어, DPI 와 서버의 상태를 어긋나게(desync) 하는 것"이다. 순수 유저스페이스에서 `send()` 순서만 뒤집으면 수신측 TCP 와 경로상 DPI 가 **똑같이 정상 재조립**하므로 desync 효과가 사라진다 — 즉 raw socket 없이는 재현 불가다. 반대로 **split 의 N-way 확장과 tls-record 다중화는 전부 순수 write** 라 제약을 100% 충족한다.

## 3. 1순위 — 제약 완전 충족 + 효과/비용 우수

### A. 분할 전략 강화

가장 적합하다. 새 분할 전략을 `-frag` 의 추가 값으로 노출하는, 기존 확장 패턴 그대로의 방식이다.

- **A-1. N-way TCP 다중분할** (`split-multi`): SNI 호스트명을 2조각이 아니라 여러 조각으로 쪼갠다. 2-way 만 가정하던 DPI 의 시그니처 매칭을 더 어렵게 만든다.
- **A-2. tls-record 다중화** (`tls-record-multi`): 핸드셰이크를 2개가 아닌 N개 TLS 레코드로 재구성한다(RFC 8446 §5.1 허용).
- **A-3. split + tls-record 하이브리드** (`split-tls`): N개 레코드로 재구성한 각 레코드를 다시 TCP 세그먼트로 분할한다.

**구현 진입점 (미래 구현 시):**

- `internal/frag/frag.go` — `WriteFirst()` 의 strategy switch 에 새 분기를 추가한다. `writeSplit`/`writeTLSRecords` 를 N-way 로 일반화하고, `makeRecord` 헬퍼는 그대로 재사용한다.
- `internal/frag/clienthello.go` — `sniSplitOffset()` 이 이미 SNI 호스트명의 시작 위치(`absNameStart`)와 길이(`nameLen`)를 계산한다. 이 파싱 walk 를 재사용해, 단일 중앙점 대신 **다중 분할점 `[]int`** 를 반환하는 헬퍼를 신설하면 글자 단위 분할까지 가능하다.
- `internal/config/config.go` — `FragStrategy` 상수를 추가한다(`config_test.go` 의 `TestFragStrategyConstants` 에 핀을 추가). `Default()` 의 기본 `FragSplit` 은 유지한다.
- `cmd/psdns/main.go` — `setFrag()` 의 허용 목록과 `bindCommon()` 의 `-frag` usage, `usage()` 출력을 동기화한다.
- **가드:** N 에 상한(예: 8)을 두고, `frag-delay` × N 으로 지연이 폭주하지 않도록 제한한다. 세그먼트가 과도하면 일부 서버가 거부할 수 있다.

**별도 평가 — `MSG_OOB`(TCP urgent) 기반 분할 (권장 안 함):** raw socket 은 아니지만, Go `net.TCPConn` 에 OOB write API 가 없어 `syscall.Conn` 으로 fd 를 꺼내 OS별로 분기해야 한다(Unix `Send(fd, b, MSG_OOB)` vs Windows URG). "3개 OS 동일·의존 최소" 와 마찰하고, 스트림 1바이트가 손상되며 서버의 URG 처리도 제각각이라 안정성이 낮다. A-1~A-3 에 집중한다.

### B. DoH 회복력

DNS 우회가 무력화되는 1차 시나리오(통신사가 DoH IP·엔드포인트 차단) 대응. 두 개의 독립 개선이다.

- **B-1. 복수 DoH 엔드포인트 hedged 폴백** (효과/난도 비 최고): 현재는 단일 엔드포인트라 자동 폴백이 없다. 여러 IP-리터럴 DoH(예: `1.1.1.1`/`8.8.8.8`/`9.9.9.9`)에 hedged(첫 엔드포인트 우선, 일정 시간 내 무응답이면 다음 것 동시 발사) 질의 후 첫 성공을 채택한다. **IP 리터럴 유지가 핵심** — DoH 연결에 SNI 가 실리지 않는 속성을 보존한다. (Cloudflare 가 먼저 차단되는 회선에서 벤더가 분산된 폴백이 실질적으로 기여한다.)
  - 진입점: `internal/resolver/resolver.go` 의 `lookup()`(이미 A/AAAA 를 goroutine 으로 병렬 질의) 에서 `doh.Exchange` 호출부를 다중 클라이언트 인터페이스 경유로 바꾼다. `internal/config/config.go` 에 `DoHURL` 다중화(콤마 구분 또는 `DoHFallbacks` 필드). `Default()` 의 단일 `1.1.1.1` 은 유지한다.
- **B-2. DoH HTTPS 핸드셰이크의 ClientHello 에도 frag 적용** (기본 구성에선 잠재적): 현재 `doh.New` 는 `http.Transport` 에 `DialContext` 만 주입하고 **`DialTLSContext` 가 없어** TLS 핸드셰이크가 Transport 내부에서 일어난다. 그래서 `-bootstrap` 으로 도메인 엔드포인트를 쓰면 ClientHello 에 SNI 가 실리는데 거기엔 분할이 전혀 안 걸린다. `DialTLSContext` 를 구현해, 평문 TCP 연결 → `TCP_NODELAY` → 첫 Write(ClientHello)만 `frag.WriteFirst` 로 위임하는 **shim `net.Conn`** 을 `tls.Client(shim, cfg)` 에 물리면 기존 frag 로직을 그대로 재사용한다.
  - 진입점: `internal/doh/client.go` 의 `New` 시그니처에 frag 인자를 추가한다(호출부는 `cmd/psdns/main.go` 의 `mustDoH` 한 곳). 기본 IP-리터럴 구성에선 SNI 가 없어 **no-op** 이므로 기본 동작은 불변이다.

## 4. 2순위 / 조건부 — 제약은 통과하나 효과 대비 복잡도 불리

- **C-1. 평문 HTTP(80) Host 헤더 분할:** 현재 HTTP 프록시는 CONNECT 외 메서드를 501 로 거부한다. 평문 포워드 프록시를 구현하고 Host 헤더 값을 분할하거나 대소문자/공백을 변형하는 것은 순수 문자열 조작이라 제약을 통과한다. 다만 트래픽이 HTTPS 위주라 실효가 낮고 프록시 표면이 크게 늘어 "경량"과 마찰한다 → 도입하더라도 "Host 값 분할"만 하는 최소형을 권장.
- **C-2. SOCKS5 UDP ASSOCIATE:** raw socket 은 아니지만, psdns 의 우회 코어인 ClientHello 분할은 TCP 세그먼테이션 개념이라 QUIC/UDP 로 그대로 옮겨가지 않는다(QUIC Initial 은 암호화되어 SNI 분할 방식이 다르다). UDP relay 의 신규 표면(소켓 수명·NAT 매핑)만 늘고 우회 효과는 없다 → 권장 안 함.
- **D. 적응형 전략 자동선택** (`--auto` 류): 핸드셰이크 직후 RST·조기 종료·타임아웃을 감지해 다음 연결에서 다른 분할 전략으로 자동 전환하고 호스트별로 학습한다. 순수 소켓 관찰이라 제약은 충족하나, "무엇을 차단으로 볼지"의 휴리스틱과 per-host 상태로 표면이 크다 → **A 가 안정화된 뒤의 후속 연구 과제**로 둔다.

## 5. 배제 — 제안하지 않는다 (위반 제약 명시)

| 후보 | 배제 사유 |
|---|---|
| disorder (역순 전송) | 효과에 **TTL=1** 이 필요하다 → 순수 write 로는 desync 가 소멸한다 → **제약 1** 위반 |
| fake packet 주입 | 가짜 패킷 = raw socket + TTL 제어 → **제약 1** 위반 |
| TTL / autottl 조작 | IP TTL 직접 설정 = 패킷 레벨 제어 → **제약 1** 위반 |
| ECH (Encrypted Client Hello) | 제약 위반은 아니나 **구조적으로 불가** — 프록시는 브라우저가 만든 ClientHello 를 `io.Copy` 로 그대로 릴레이할 뿐이라 SNI 를 암호화해 주입할 수 없다. ECH 는 클라이언트 TLS 스택이 직접 암호화된 ClientHello 를 생성하고 공개키를 DNS HTTPS RR 로 분배받아야 성립하며, 이는 브라우저/OS 의 몫이다. (psdns 의 DoH 리졸버가 HTTPS RR 응답을 그대로 전달하는 것까진 가능하나, 그걸 ECH 로 쓰는 주체는 브라우저다.) |
| DoQ / DoH3 (DNS over QUIC/HTTP3) | `quic-go` 등 무거운 의존성 추가 → **제약 2** 위반. 현 RFC 8484 DoH(TCP/HTTP2)로 충분하며 QUIC 의 회복력 이점이 의존성 무게를 정당화하지 못한다. (연구 메모로만 보존.) |
| SOCKS5 UDP relay | 우회 효과 없이 신규 표면만 증가 → **제약 2·3** 과 마찰 |

## 6. 권장 실행 순서

차단이 강화되면 아래 순서로 착수한다. 전부 기존 전략과 병렬 opt-in 이며 `config.Default()` 는 불변이다.

1. **A** — N-way 다중분할 · tls-record 다중화 · split-tls 하이브리드 (`internal/frag` 확장)
2. **B-1** — 복수 DoH 엔드포인트 hedged 폴백 (`internal/resolver`·`internal/doh`·`internal/config`)
3. **B-2** — DoH ClientHello 에 frag 적용 (`internal/doh` 의 `DialTLSContext` 훅)
4. (선택) **C-1** — 평문 HTTP Host 분할 최소형
5. (후속 연구) **D** — 적응형 자동선택

## 7. 참고 출처

- byedpi — <https://github.com/hufrea/byedpi> (README 의 split/disorder/fake/tlsrec/mod-http, DeepWiki 의 hook 레벨 구현)
- zapret — <https://github.com/bol-van/zapret> (DeepWiki "DPI Circumvention Techniques": fake/disorder 가 패킷 레벨임)
- RFC 9849 — TLS Encrypted Client Hello — <https://www.rfc-editor.org/rfc/rfc9849.html>
- Mozilla, Encrypted Client Hello — <https://wiki.mozilla.org/Security/Encrypted_Client_Hello>
- XTLS/Xray-core Discussion #5969 — SNI-spoofing 기법 비교 (순수 fragment vs TTL/seq 주입의 효과 차이)
- OONI — Measuring DoT/DoH Blocking — <https://ooni.org/post/2022-doh-dot-paper-dnsprivacy21/>
