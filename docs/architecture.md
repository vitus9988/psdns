# 내부 구조

psdns 의 모듈 의존 관계와 요청 한 건이 처리되는 흐름을 시각화한다. 디렉터리별 한 줄 설명은 [README §구조](../README.md#구조)에 있고, 설계 *이유*(Why)는 [CLAUDE.md](../CLAUDE.md)에 있다 — 여기서는 **어떻게 연결되고 흐르는가**만 다룬다.

## 모듈 의존 관계

차단 우회 코어(`proxy`·`frag`·`resolver`·`dnssrv`·`doh`)는 순수 유저스페이스 소켓만 쓰며 `CGO_ENABLED=0` 으로 빌드된다. GUI 전용 묶음(Wails·트레이·시스템 프록시·자동 업데이트)은 CLI 바이너리로 새지 않도록 격리돼 있다.

```mermaid
flowchart TD
    subgraph entry["진입점"]
        CLI["cmd/psdns<br/>resolve · proxy · run · update"]
        GUIBIN["cmd/psdns-gui"]
    end

    subgraph core["차단 우회 코어 (순수 유저스페이스 · CGO-free)"]
        PROXY["internal/proxy<br/>HTTP CONNECT · SOCKS5 · 평문 · relay"]
        FRAG["internal/frag<br/>ClientHello 파싱 · 분할"]
        RESOLVER["internal/resolver<br/>host→IP · TTL 캐시"]
        DNSSRV["internal/dnssrv<br/>로컬 DNS UDP/TCP"]
        DOH["internal/doh<br/>DoH 클라이언트 (RFC 8484)"]
        CONFIG["internal/config<br/>런타임 설정"]
    end

    subgraph guionly["GUI 전용 (CLI 비의존 · cgo/네이티브)"]
        GUI["internal/gui<br/>Wails 바인딩 · 트레이 · 닫기"]
        SUP["internal/supervisor<br/>start/stop 오케스트레이션"]
        UICONF["internal/uiconfig<br/>UI DTO ↔ config"]
        SYSPROXY["internal/sysproxy<br/>OS 웹 프록시 설정/복원"]
        SELFUP["internal/selfupdate<br/>릴리즈 자동 업데이트"]
    end

    CLI --> DNSSRV
    CLI --> PROXY
    CLI --> RESOLVER
    CLI --> DOH
    GUIBIN --> GUI
    GUI --> SUP
    GUI --> UICONF
    GUI --> SYSPROXY
    GUI --> SELFUP
    SUP --> DNSSRV
    SUP --> PROXY
    SUP --> RESOLVER
    SUP --> DOH
    PROXY --> RESOLVER
    PROXY --> FRAG
    PROXY --> CONFIG
    RESOLVER --> DOH
    DNSSRV --> DOH
    FRAG --> CONFIG
    UICONF --> CONFIG
```

## 흐름 (a): 브라우저 HTTPS → HTTP CONNECT 프록시 (DNS + SNI 동시 우회)

`proxy` 가 클라이언트↔업스트림 TCP 를 종단 분리하므로 업스트림으로 나가는 세그먼트 경계를 완전히 제어한다. 이름 해석은 프록시 내부에서 DoH 로 처리해 위조 DNS 를 거치지 않는다.

```mermaid
sequenceDiagram
    participant B as 브라우저
    participant H as HTTPProxy.handle<br/>(proxy/http.go)
    participant R as resolver.Resolve
    participant D as doh.Exchange
    participant U as 대상 서버

    B->>H: CONNECT target:443
    H->>R: handleConnect → dialUpstream(target)
    R->>D: A/AAAA 질의 (캐시 미스 시)
    D-->>R: IP 목록 (DoH over HTTPS → 1.1.1.1)
    R-->>H: IPs
    H->>U: TCP dial (TCP_NODELAY 활성)
    H-->>B: 200 Connection Established
    B->>H: TLS ClientHello (첫 레코드)
    Note over H: relay: readFirstRecord →<br/>frag.WriteFirst(split/tls-record)
    H->>U: 세그먼트 1 (SNI 앞부분)
    H->>U: 세그먼트 2 (SNI 뒷부분)
    Note over U: 경로상 DPI 가 평문 SNI 파싱 실패
    U-->>B: 이후 양방향 io.Copy (verbatim)
```

평문 HTTP(비-CONNECT)는 `handlePlain` 으로 분기해 origin-form 재작성 후 DoH 해석·릴레이만 한다 — TLS 가 없어 분할 경로를 타지 않는다.

## 흐름 (b): DNS 질의 → 로컬 리졸버 → DoH (DNS 위조 우회)

```mermaid
sequenceDiagram
    participant OS as OS 리졸버
    participant S as dnssrv.handle<br/>(dnssrv/server.go)
    participant D as doh.Exchange
    participant C as DoH 서버 (1.1.1.1)

    OS->>S: DNS 질의 (UDP/TCP :53)
    S->>D: Exchange(req)
    D->>C: POST /dns-query (application/dns-message)
    C-->>D: DNS 응답
    D-->>S: *dns.Msg
    Note over S: resp.Id = req.Id 보존
    S-->>OS: 응답 (통신사 DNS 미경유)
```

기본 DoH 엔드포인트가 IP 리터럴(`1.1.1.1`)이라 DoH 연결 자체에는 SNI 가 실리지 않고 부트스트랩 DNS 도 필요 없다.

> 측정 지점(지연·캐시 적중·프로파일링)은 [measurements.md](measurements.md), 향후 우회 전략 로드맵은 [bypass-roadmap.md](bypass-roadmap.md) 참고.
