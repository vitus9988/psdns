package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
	"github.com/vitus9988/psdns/internal/resolver"
)

// orderedAResolver returns a Resolver whose DoH upstream answers A queries with
// the given IPv4 literals in order (and AAAA with nothing), giving dialUpstream
// a deterministic IP ordering to exercise its fall-back loop.
func orderedAResolver(t *testing.T, ips ...string) *resolver.Resolver {
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
			for _, ip := range ips {
				resp.Answer = append(resp.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP(ip),
				})
			}
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

// TestDialUpstreamFallsBackToSecondIP verifies dialUpstream's per-IP loop: when
// the first resolved address is unreachable it must try the next. The first IP
// is TEST-NET-1 (192.0.2.0/24, RFC 5737, guaranteed unroutable) so its dial
// fails within the timeout, then the loopback listener accepts.
func TestDialUpstreamFallsBackToSecondIP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	res := orderedAResolver(t, "192.0.2.1", "127.0.0.1")
	conn, err := dialUpstream(context.Background(), res, "host.example", port, 700*time.Millisecond)
	if err != nil {
		t.Fatalf("dialUpstream should fall back to the reachable IP, got: %v", err)
	}
	_ = conn.Close()
}

// TestDialUpstreamAllIPsFail verifies the loop surfaces the last error when no
// resolved address is reachable.
func TestDialUpstreamAllIPsFail(t *testing.T) {
	res := orderedAResolver(t, "192.0.2.1", "192.0.2.2")
	_, err := dialUpstream(context.Background(), res, "host.example", "443", 300*time.Millisecond)
	if err == nil {
		t.Fatal("dialUpstream should fail when every IP is unreachable")
	}
}
