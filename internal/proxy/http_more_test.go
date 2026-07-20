package proxy_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
	"github.com/vitus9988/psdns/internal/proxy"
	"github.com/vitus9988/psdns/internal/resolver"
)

// emptyAnswerResolver returns a Resolver whose DoH upstream answers every query
// with no records, so every Resolve (and thus dialUpstream) fails.
func emptyAnswerResolver(t *testing.T) *resolver.Resolver {
	t.Helper()
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
	return resolver.New(c)
}

// originServer runs a plaintext HTTP origin that serves each accepted
// connection with handle. It accepts until the test ends.
func originServer(t *testing.T, handle func(net.Conn)) (port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen origin: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port
}

// TestHTTPListenAndServeBindError covers the CLI bind-failure path: an
// unbindable listen address must be returned as an error, not served.
func TestHTTPListenAndServeBindError(t *testing.T) {
	cfg := testConfig("203.0.113.1:1", "127.0.0.1:0") // TEST-NET-3, not local
	hp := proxy.NewHTTP(nil, cfg)
	if err := hp.ListenAndServe(); err == nil {
		t.Fatal("expected bind error for non-local listen address")
	}
}

// TestSOCKSListenAndServeBindError is the SOCKS twin of the bind-failure path.
func TestSOCKSListenAndServeBindError(t *testing.T) {
	cfg := testConfig("127.0.0.1:0", "203.0.113.1:1")
	sp := proxy.NewSOCKS(nil, cfg)
	if err := sp.ListenAndServe(); err == nil {
		t.Fatal("expected bind error for non-local listen address")
	}
}

// TestHTTPConnectDialFailure verifies CONNECT answers 502 when the target
// cannot be resolved/dialed.
func TestHTTPConnectDialFailure(t *testing.T) {
	res := emptyAnswerResolver(t)
	addr := startHTTP(t, res)

	conn := waitListen(t, addr)
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	target := "unresolvable.invalid:443"
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT status = %d, want 502", resp.StatusCode)
	}
}

// TestHTTPPlainDialFailure verifies a plaintext proxy request answers 502 when
// the origin cannot be resolved/dialed.
func TestHTTPPlainDialFailure(t *testing.T) {
	res := emptyAnswerResolver(t)
	addr := startHTTP(t, res)

	conn := waitListen(t, addr)
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	_, _ = fmt.Fprint(conn, "GET http://unresolvable.invalid/ HTTP/1.1\r\nHost: unresolvable.invalid\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

// TestHTTPPlainMissingHost verifies an origin-form request with no Host at all
// is rejected with 400 instead of being forwarded nowhere.
func TestHTTPPlainMissingHost(t *testing.T) {
	res := mockResolver(t, "127.0.0.1")
	addr := startHTTP(t, res)

	conn := waitListen(t, addr)
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	// HTTP/1.0 permits a missing Host header, leaving no target host anywhere.
	_, _ = fmt.Fprint(conn, "GET /nohost HTTP/1.0\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHTTPPlainKeepAlive drives two plaintext requests over one client
// connection: the handler must loop (fresh upstream per request) and only
// return once the client stops sending.
func TestHTTPPlainKeepAlive(t *testing.T) {
	port := originServer(t, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return
		}
		_, _ = fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	})

	res := mockResolver(t, "127.0.0.1")
	addr := startHTTP(t, res)

	conn := waitListen(t, addr)
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	br := bufio.NewReader(conn)

	target := "blocked.example.com:" + port
	for i := 0; i < 2; i++ {
		_, _ = fmt.Fprintf(conn, "GET http://%s/req%d HTTP/1.1\r\nHost: %s\r\n\r\n", target, i, target)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
		if err != nil {
			t.Fatalf("read response #%d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("response #%d = %d %q, want 200 %q", i, resp.StatusCode, body, "ok")
		}
	}
}

// TestHTTPPlainBadUpstreamResponse verifies the handler gives up cleanly (closes
// the client) when the origin replies with something that is not HTTP.
func TestHTTPPlainBadUpstreamResponse(t *testing.T) {
	port := originServer(t, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return
		}
		_, _ = fmt.Fprint(conn, "TOTALLY-NOT-HTTP\r\n\r\n")
	})

	res := mockResolver(t, "127.0.0.1")
	addr := startHTTP(t, res)

	conn := waitListen(t, addr)
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	target := "blocked.example.com:" + port
	_, _ = fmt.Fprintf(conn, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet}); err == nil {
		t.Fatal("expected the proxy to close the connection after a bad upstream response")
	}
}
