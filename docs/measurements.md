# 측정 가이드 (벤치마크 · 프로파일링 · 현장 결과)

psdns 의 차단 우회 **효과는 통신사 DPI 구현에 종속**되어 100% 보장되지 않는다(README 참고). 그래서 "얼마나 잘 되는가"는 말이 아니라 **재현 가능한 수치**로 남겨야 한다. 이 문서는 두 종류의 데이터를 다룬다.

| 종류 | 누가 만드나 | 도구 | 어디에 기록 |
|---|---|---|---|
| **자동 측정** — 지연/메모리/CPU(할당) | 코드(아무 머신) | `scripts/bench.sh`, `-pprof` | 아래 [벤치마크 결과](#벤치마크-결과) |
| **현장 측정** — ISP별 성공/실패, 실제 지연 | 사람(실제 회선) | 브라우저/`curl`, [측정 절차](#현장-측정-isp사이트) | 아래 [현장 결과표](#현장-결과표) |

> 자동 측정은 fragmentation·캐시의 *비용*을 보여주고, 현장 측정은 우회가 *실제로 통하는지*를 보여준다. 둘은 서로 대체할 수 없다.

---

## 벤치마크

### 실행

```sh
scripts/bench.sh                 # frag · resolver · proxy 전체
scripts/bench.sh -cpuprofile     # internal/frag 를 프로파일 (cpu.prof, mem.prof)

# 개별 패키지만:
go test -run='^$' -bench=. -benchmem ./internal/frag/
```

측정 대상:

- `internal/frag` — `WriteFirst`(none/split/tls-record 전략별)와 `sniSplitOffset`(SNI 파서). fragmentation 자체 비용.
- `internal/resolver` — `Resolve` 캐시 히트와 IP 리터럴 경로(정상 트래픽의 지배적 경로).
- `internal/proxy` — `readFirstRecord`(연결당 첫 TLS 레코드 프레이밍)와 `relay` 처리량(인메모리 파이프 기준).

### 벤치마크 결과

> 아래 수치는 `scripts/bench.sh` 출력으로 **갱신**한다. 절대값은 머신마다 다르므로 항상 측정 환경을 함께 적는다.

**측정 환경:** Apple M2 Ultra · darwin/arm64 · go1.26.4 · 2026-06-23

| 벤치마크 | ns/op | B/op | allocs/op | 비고 |
|---|---:|---:|---:|---|
| `WriteFirst/none` | 2.4 | 0 | 0 | 무수정 패스스루(분할 안 함) |
| `WriteFirst/split` | 13.4 | 0 | 0 | SNI 파싱 후 2개 슬라이스 write — 할당 없음 |
| `WriteFirst/tls-record` | 45.9 | 96 | 2 | 2개 TLS 레코드 재구성(`makeRecord` 할당) |
| `SNISplitOffset` | 7.9 | 0 | 0 | ClientHello SNI 파서(split·tls-record 공통) |
| `ResolveCacheHit` | 88.2 | 64 | 2 | 캐시 히트(해석된 호스트의 일반 경로) |
| `ResolveIPLiteral` | 48.7 | 40 | 2 | IP 리터럴 단락 경로 |
| `ReadFirstRecord` | 205.7 | 1093 | 3 | 연결당 첫 레코드 프레이밍(버퍼 포함) |
| `RelayThroughput` | 12707 | 36147 | 32 | 인메모리 릴레이 1청크(32KiB) — 상대 지표 |

**해석:**

- 기본 전략 `split`은 한 자릿수 나노초대이며 **할당이 없다** — fragmentation 은 연결당 1회뿐이라 처리량에 무의미한 비용이다.
- `tls-record`만 할당(레코드 2개 재구성)이 있으나 절대량이 작다.
- `RelayThroughput`은 `net.Pipe` 기반 **인메모리 상대 지표**다. 실제 처리량은 커널·네트워크·`io.Copy` 버퍼에 지배되므로 절대 MB/s 로 읽지 말 것.

---

## 프로파일링 (CPU · 메모리)

장시간 실행 중인 프로세스를 프로파일하려면 옵트인 `-pprof` 플래그를 쓴다(기본 꺼짐 → 평소 오버헤드 0).

```sh
psdns proxy -pprof 127.0.0.1:6060
# 다른 터미널에서:
go tool pprof http://127.0.0.1:6060/debug/pprof/heap       # 메모리(힙)
go tool pprof http://127.0.0.1:6060/debug/pprof/profile    # CPU(30초 샘플)
go tool pprof http://127.0.0.1:6060/debug/pprof/goroutine  # 고루틴 수
```

벤치마크에서 프로파일을 뽑으려면:

```sh
scripts/bench.sh -cpuprofile      # cpu.prof / mem.prof
go tool pprof cpu.prof
```

> `-pprof`는 디버그·측정용이다. 차단 우회 경로에 포함되지 않으며, 플래그를 주지 않으면 리스너가 생기지 않는다.

---

## 현장 측정 (ISP·사이트)

ISP별 성공/실패와 실제 지연은 **실제 회선에서 사람이** 측정해야 한다. 아래 절차로 측정하고 [현장 결과표](#현장-결과표)에 기록한다.

### 준비

1. 빌드/릴리즈한 `psdns proxy`(또는 GUI)를 실행한다(기본 `127.0.0.1:8080`).
2. 비교를 위해 **분할 전략을 바꿔가며** 측정한다: `-frag none`(우회 없음, 대조군) / `-frag split`(기본) / `-frag tls-record`.

### 성공/실패 판정 기준

차단된 사이트에 대해, 다음을 **성공**으로 본다.

- 차단 페이지(`warning.or.kr` 등)로 리다이렉트되지 **않고** 대상 사이트의 실제 응답을 받는다.
- HTTPS 의 경우 TLS 핸드셰이크가 RST 없이 완료된다.

**실패**: 연결 RST/타임아웃, 또는 차단 페이지 응답.

### 지연 측정 (`curl`)

동일 사이트를 **직접**과 **프록시 경유**로 각각 재어 추가 지연(Δ)을 본다.

```sh
# 직접(차단되지 않은 사이트 기준선) — total 시간
curl -o /dev/null -s -w 'connect=%{time_connect} ttfb=%{time_starttransfer} total=%{time_total}\n' https://example.com

# psdns 프록시 경유
curl -x http://127.0.0.1:8080 -o /dev/null -s -w 'connect=%{time_connect} ttfb=%{time_starttransfer} total=%{time_total}\n' https://example.com
```

각 3~5회 측정해 중앙값을 기록한다. `Δttfb = (프록시 ttfb) − (직접 ttfb)`.

### 기록 규칙

각 행에 **ISP · 사이트 · 전략 · 결과 · Δttfb · 측정일 · psdns 버전**을 남긴다. ISP/지역은 식별 가능한 수준(예: "KT 유선 / 서울")으로만 적고 개인 식별 정보는 넣지 않는다.

### 현장 결과표

> 아래는 템플릿이다. 실측으로 행을 채운다.

| ISP / 회선 | 사이트 | 전략 | 결과 | Δttfb | 측정일 | 버전 |
|---|---|---|---|---:|---|---|
| _예: KT 유선 / 서울_ | _example.blocked_ | split | ✅ 성공 | +18ms | 2026-06-23 | v0.7.0 |
| | | tls-record | | | | |
| | | none | ❌ 차단 | — | | |

---

## 라이브 회귀 테스트 (실제 DoH)

기본 CI 는 네트워크가 없어 실제 DoH 엔드포인트를 타지 않는다. 실제 인터넷에서 기본 엔드포인트(`1.1.1.1`) 왕복을 확인하려면 수동으로:

```sh
go test -tags live ./internal/proxy/
```

> 네트워크가 필요하므로 일반 `go test`·CI 에서는 제외된다(빌드 태그 `live`).
