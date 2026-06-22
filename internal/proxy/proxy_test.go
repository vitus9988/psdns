package proxy_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/doh"
	"github.com/vitus9988/psdns/internal/proxy"
	"github.com/vitus9988/psdns/internal/resolver"
)

// mockResolver returns a Resolver whose DoH upstream answers every A query with
// the given IP literal, so dialUpstream connects to a local test server.
func mockResolver(t *testing.T, ip string) *resolver.Resolver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil || len(q.Question) == 0 {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		resp := new(dns.Msg)
		resp.SetReply(q)
		if q.Question[0].Qtype == dns.TypeA {
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP(ip),
			})
		}
		packed, _ := resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(packed)
	}))
	t.Cleanup(srv.Close)
	c, err := doh.New(srv.URL+"/dns-query", "", 5*time.Second)
	if err != nil {
		t.Fatalf("doh.New: %v", err)
	}
	return resolver.New(c)
}

// recordingUpstream is a TCP server that, for the first accepted connection,
// records the boundaries of each Read from the client side (to detect TLS
// ClientHello fragmentation) and echoes a fixed greeting back so the proxy's
// upstream->client relay path is exercised.
type recordingUpstream struct {
	ln       net.Listener
	mu       sync.Mutex
	reads    [][]byte // each client->upstream Read, in order
	greeting []byte
	done     chan struct{}
}

func newRecordingUpstream(t *testing.T, greeting string) *recordingUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	u := &recordingUpstream{ln: ln, greeting: []byte(greeting), done: make(chan struct{})}
	go u.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return u
}

func (u *recordingUpstream) serve() {
	defer close(u.done)
	conn, err := u.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	// Send greeting first so the client (proxy relay) has something to forward
	// back regardless of read timing.
	_, _ = conn.Write(u.greeting)

	buf := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			u.mu.Lock()
			u.reads = append(u.reads, append([]byte(nil), buf[:n]...))
			u.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (u *recordingUpstream) port() string {
	_, p, _ := net.SplitHostPort(u.ln.Addr().String())
	return p
}

// readBoundaries returns a snapshot of recorded client->upstream reads.
func (u *recordingUpstream) readBoundaries() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([][]byte, len(u.reads))
	copy(out, u.reads)
	return out
}

// buildClientHello constructs a minimal well-formed TLS ClientHello record with
// the given SNI (same shape as frag's test helper).
func buildClientHello(sni string) []byte {
	name := []byte(sni)
	entry := []byte{0x00, byte(len(name) >> 8), byte(len(name))}
	entry = append(entry, name...)
	snList := append([]byte{byte(len(entry) >> 8), byte(len(entry))}, entry...)
	ext := append([]byte{0x00, 0x00, byte(len(snList) >> 8), byte(len(snList))}, snList...)

	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00)
	body = append(body, 0x00, 0x02, 0x13, 0x01)
	body = append(body, 0x01, 0x00)
	body = append(body, byte(len(ext)>>8), byte(len(ext)))
	body = append(body, ext...)

	hs := []byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	hs = append(hs, body...)

	rec := []byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}
	return append(rec, hs...)
}

// waitListen dials the proxy listener until it is accepting, returning a usable
// connection.
func waitListen(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			return conn
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("proxy listener at %s never came up", addr)
	return nil
}

// fragDelay forces the two ClientHello fragments to land in separate TCP reads
// at the upstream, so the E2E fragmentation assertion is deterministic rather
// than dependent on Nagle/coalescing. The write-boundary correctness itself is
// covered deterministically by the frag package's own unit tests.
const fragDelay = 60 * time.Millisecond

func testConfig(listenHTTP, listenSOCKS string) config.Config {
	c := config.Default()
	c.ProxyListen = listenHTTP
	c.SocksListen = listenSOCKS
	c.Frag = config.FragSplit
	c.FragDelay = fragDelay
	c.Timeout = 3 * time.Second
	return c
}

// startSOCKS starts a SOCKS proxy on a fresh loopback port and returns its
// address once it is accepting connections. The proxy is closed on cleanup.
func startSOCKS(t *testing.T, res *resolver.Resolver) string {
	t.Helper()
	addr := pickPort(t)
	cfg := testConfig("127.0.0.1:0", addr)
	sp := proxy.NewSOCKS(res, cfg)
	go func() { _ = sp.ListenAndServe() }()
	t.Cleanup(func() { _ = sp.Close() })
	waitListen(t, addr).Close()
	return addr
}

// startHTTP starts an HTTP CONNECT proxy on a fresh loopback port and returns
// its address once it is accepting connections. The proxy is closed on cleanup.
func startHTTP(t *testing.T, res *resolver.Resolver) string {
	t.Helper()
	addr := pickPort(t)
	cfg := testConfig(addr, "127.0.0.1:0")
	hp := proxy.NewHTTP(res, cfg)
	go func() { _ = hp.ListenAndServe() }()
	t.Cleanup(func() { _ = hp.Close() })
	waitListen(t, addr).Close()
	return addr
}

// TestSOCKS5EndToEnd drives a full SOCKS5 no-auth CONNECT handshake over a real
// loopback socket, then sends a ClientHello and asserts: the handshake replies
// are correct, the upstream sees the ClientHello fragmented (split strategy ->
// SNI not wholly in the first read), and the upstream greeting is relayed back.
func TestSOCKS5EndToEnd(t *testing.T) {
	const sni = "blocked.example.com"
	const greeting = "SERVER-HELLO-BYTES"

	upstream := newRecordingUpstream(t, greeting)
	res := mockResolver(t, "127.0.0.1")
	addr := startSOCKS(t, res)

	conn := waitListen(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	// Greeting: VER=5, NMETHODS=1, METHOD=0 (no auth).
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	methodSel := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodSel); err != nil {
		t.Fatalf("read method selection: %v", err)
	}
	if methodSel[0] != 0x05 || methodSel[1] != 0x00 {
		t.Fatalf("method selection = %v, want [5 0]", methodSel)
	}

	// Request: VER=5, CMD=CONNECT(1), RSV=0, ATYP=domain(3), len, domain, port.
	pn, err := strconv.Atoi(upstream.port())
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(sni))}
	req = append(req, []byte(sni)...)
	var pb [2]byte
	binary.BigEndian.PutUint16(pb[:], uint16(pn))
	req = append(req, pb[:]...)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Reply: VER=5, REP=0 (success), RSV, ATYP, BND.ADDR, BND.PORT (10 bytes).
	rep := make([]byte, 10)
	if _, err := io.ReadFull(conn, rep); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if rep[0] != 0x05 || rep[1] != 0x00 {
		t.Fatalf("SOCKS reply = %v, want success (rep=0)", rep[:2])
	}

	// Now the tunnel is open: send the ClientHello as the first payload.
	hello := buildClientHello(sni)
	if _, err := conn.Write(hello); err != nil {
		t.Fatalf("write ClientHello: %v", err)
	}

	// Reverse path: we should receive the upstream greeting.
	got := make([]byte, len(greeting))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read relayed greeting: %v", err)
	}
	if string(got) != greeting {
		t.Fatalf("relayed greeting = %q, want %q", got, greeting)
	}

	assertFragmented(t, upstream, hello, sni)
}

// TestHTTPConnectEndToEnd drives a full HTTP CONNECT tunnel and asserts the 200
// response, fragmented ClientHello upstream, and relayed greeting.
func TestHTTPConnectEndToEnd(t *testing.T) {
	const sni = "blocked.example.org"
	const greeting = "UPSTREAM-GREETING-1"

	upstream := newRecordingUpstream(t, greeting)
	res := mockResolver(t, "127.0.0.1")
	addr := startHTTP(t, res)

	conn := waitListen(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	target := net.JoinHostPort(sni, upstream.port())
	connectReq := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err := conn.Write([]byte(connectReq)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	hello := buildClientHello(sni)
	if _, err := conn.Write(hello); err != nil {
		t.Fatalf("write ClientHello: %v", err)
	}

	// Greeting may have been buffered into br during ReadResponse; read from br.
	got := make([]byte, len(greeting))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read relayed greeting: %v", err)
	}
	if string(got) != greeting {
		t.Fatalf("relayed greeting = %q, want %q", got, greeting)
	}

	assertFragmented(t, upstream, hello, sni)
}

// TestHTTPPlainForward verifies a plaintext (non-CONNECT) request is forwarded
// to the DoH-resolved origin and the origin's response is relayed back, with the
// request rewritten to origin form and the hop-by-hop proxy header stripped.
func TestHTTPPlainForward(t *testing.T) {
	// A minimal upstream origin: capture the forwarded request, reply with a body.
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	defer upLn.Close()

	type forwarded struct {
		reqURI   string
		hasProxy bool
		hostHdr  string
	}
	gotCh := make(chan forwarded, 1)
	go func() {
		conn, err := upLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		gotCh <- forwarded{
			reqURI:   r.RequestURI,
			hasProxy: r.Header.Get("Proxy-Connection") != "",
			hostHdr:  r.Host,
		}
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nhi")
	}()

	_, upPort, _ := net.SplitHostPort(upLn.Addr().String())
	res := mockResolver(t, "127.0.0.1") // every host resolves to loopback
	addr := startHTTP(t, res)

	conn := waitListen(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	target := "blocked.example.com:" + upPort
	fmt.Fprintf(conn, "GET http://%s/path?x=1 HTTP/1.1\r\nHost: %s\r\nProxy-Connection: keep-alive\r\n\r\n", target, target)

	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "hi" {
		t.Fatalf("body = %q, want %q", body, "hi")
	}

	select {
	case g := <-gotCh:
		if g.reqURI != "/path?x=1" {
			t.Errorf("upstream request URI = %q, want origin-form /path?x=1", g.reqURI)
		}
		if g.hasProxy {
			t.Errorf("Proxy-Connection header leaked to upstream")
		}
		if g.hostHdr != target {
			t.Errorf("upstream Host = %q, want %q", g.hostHdr, target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never received the forwarded request")
	}
}

// TestSOCKS5UnsupportedCommand verifies a non-CONNECT command yields reply 0x07.
func TestSOCKS5UnsupportedCommand(t *testing.T) {
	res := mockResolver(t, "127.0.0.1")
	addr := startSOCKS(t, res)

	conn := waitListen(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	methodSel := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodSel); err != nil {
		t.Fatalf("read method selection: %v", err)
	}
	// CMD=BIND(0x02) is not supported.
	req := []byte{0x05, 0x02, 0x00, 0x01, 127, 0, 0, 1, 0x00, 0x50}
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	rep := make([]byte, 10)
	if _, err := io.ReadFull(conn, rep); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if rep[1] != 0x07 {
		t.Fatalf("reply code = %#x, want 0x07 (command not supported)", rep[1])
	}
}

// TestDialUpstreamResolveError exercises the dial failure path via the SOCKS
// handler: a resolver whose DoH always fails should make CONNECT return 0x05.
func TestSOCKS5DialFailure(t *testing.T) {
	// Resolver backed by a DoH server that returns no answers -> Resolve errors.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		q := new(dns.Msg)
		_ = q.Unpack(body)
		resp := new(dns.Msg)
		resp.SetReply(q) // empty answer -> "no addresses"
		packed, _ := resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(packed)
	}))
	t.Cleanup(srv.Close)
	c, err := doh.New(srv.URL+"/dns-query", "", 3*time.Second)
	if err != nil {
		t.Fatalf("doh.New: %v", err)
	}
	res := resolver.New(c)

	addr := startSOCKS(t, res)

	conn := waitListen(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	methodSel := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodSel); err != nil {
		t.Fatalf("read method selection: %v", err)
	}
	domain := "unresolvable.invalid"
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(domain))}
	req = append(req, []byte(domain)...)
	req = append(req, 0x01, 0xBB) // port 443
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	rep := make([]byte, 10)
	if _, err := io.ReadFull(conn, rep); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if rep[1] != 0x05 {
		t.Fatalf("reply code = %#x, want 0x05 (connection refused)", rep[1])
	}
}

// TestSOCKS5BadVersion verifies a non-0x05 greeting version causes the proxy to
// close the connection immediately without a reply.
func TestSOCKS5BadVersion(t *testing.T) {
	res := mockResolver(t, "127.0.0.1")
	addr := startSOCKS(t, res)

	conn := waitListen(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// SOCKS4 version byte (0x04) is rejected by the SOCKS5 handler.
	if _, err := conn.Write([]byte{0x04, 0x01, 0x00}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The proxy closes without writing; the next read must hit EOF.
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err == nil {
		t.Fatalf("expected connection close (EOF) after bad version, read %v", buf)
	}
}

// TestHTTPMalformedRequest verifies a malformed (non-HTTP) request line causes
// the proxy to close the connection without a response.
func TestHTTPMalformedRequest(t *testing.T) {
	res := mockResolver(t, "127.0.0.1")
	addr := startHTTP(t, res)

	conn := waitListen(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// Garbage that http.ReadRequest cannot parse -> handler closes silently.
	if _, err := conn.Write([]byte("not-a-valid-http-request-line\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err == nil {
		t.Fatalf("expected connection close after malformed request, read %v", buf)
	}
}

// TestCloseConcurrentWithStartupIsRaceFree verifies Close() may be called
// concurrently with (or before) ListenAndServe — as cmd/psdns's signal handler
// does — without a data race on the listener field. The field is guarded by a
// mutex + closed flag, so this must stay clean under -race:
//
//	go test -race -count=10 -run TestCloseConcurrentWithStartupIsRaceFree
func TestCloseConcurrentWithStartupIsRaceFree(t *testing.T) {
	for i := 0; i < 20; i++ {
		cfg := config.Default()
		cfg.ProxyListen = "127.0.0.1:0"
		cfg.SocksListen = "127.0.0.1:0"
		hp := proxy.NewHTTP(nil, cfg)
		sp := proxy.NewSOCKS(nil, cfg)
		go func() { _ = hp.ListenAndServe() }()
		go func() { _ = sp.ListenAndServe() }()
		_ = hp.Close()
		_ = sp.Close()
	}
}

// assertFragmented checks that the upstream observed the ClientHello as more
// than one segment and that the SNI host name is not wholly contained in the
// first client->upstream read (the split strategy must straddle it). It allows
// for the reverse-path greeting timing by reassembling all recorded reads and
// comparing to the original ClientHello.
func assertFragmented(t *testing.T, u *recordingUpstream, hello []byte, sni string) {
	t.Helper()
	var reads [][]byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		reads = u.readBoundaries()
		var total int
		for _, r := range reads {
			total += len(r)
		}
		if total >= len(hello) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	var merged []byte
	for _, r := range reads {
		merged = append(merged, r...)
	}
	if !bytes.Contains(merged, hello) {
		t.Fatalf("upstream did not receive the full ClientHello (got %d bytes)", len(merged))
	}
	if len(reads) < 2 {
		t.Fatalf("expected ClientHello to arrive in >=2 segments (fragmented), got %d", len(reads))
	}
	if bytes.Contains(reads[0], []byte(sni)) {
		t.Fatalf("first upstream segment contains the whole SNI %q; fragmentation failed", sni)
	}
}

// pickPort finds a currently-free loopback TCP port and returns host:port. There
// is an inherent bind race, but the proxy binds immediately and tests poll via
// waitListen before use.
func pickPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
