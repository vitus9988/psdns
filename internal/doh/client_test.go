package doh_test

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
)

func TestExchange(t *testing.T) {
	const want = "93.184.216.34"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil || len(q.Question) == 0 {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		resp := new(dns.Msg)
		resp.SetReply(q)
		if q.Question[0].Qtype == dns.TypeA {
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP(want),
			})
		}
		packed, err := resp.Pack()
		if err != nil {
			http.Error(w, "pack", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(packed)
	}))
	defer srv.Close()

	c, err := doh.New(srv.URL+"/dns-query", "", 5*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	resp, err := c.Exchange(context.Background(), q)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	var got string
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok {
			got = a.A.String()
		}
	}
	if got != want {
		t.Fatalf("got A %q, want %q", got, want)
	}
}

func TestNewRejectsNonHTTP(t *testing.T) {
	if _, err := doh.New("ftp://example.com/dns-query", "", time.Second); err == nil {
		t.Fatalf("expected error for non-http(s) scheme")
	}
}

// TestExchangeUsesBootstrap verifies that with a bootstrap address set, the
// client dials the bootstrap instead of resolving the endpoint host. The
// endpoint host is a name that does not resolve, so a successful exchange (and
// the endpoint hostname arriving in the Host header) proves system DNS was
// bypassed via the bootstrap.
func TestExchangeUsesBootstrap(t *testing.T) {
	const want = "203.0.113.7"
	var gotHostHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHostHeader = r.Host
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil || len(q.Question) == 0 {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		resp := new(dns.Msg)
		resp.SetReply(q)
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP(want),
		})
		packed, _ := resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(packed)
	}))
	defer srv.Close()

	c, err := doh.New("http://doh.invalid.test/dns-query", srv.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	resp, err := c.Exchange(context.Background(), q)
	if err != nil {
		t.Fatalf("Exchange via bootstrap: %v", err)
	}
	var got string
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok {
			got = a.A.String()
		}
	}
	if got != want {
		t.Fatalf("got A %q, want %q", got, want)
	}
	if gotHostHeader != "doh.invalid.test" {
		t.Fatalf("Host header = %q, want endpoint hostname doh.invalid.test", gotHostHeader)
	}
}

// TestExchangeTimeout verifies the per-request timeout is enforced when the
// upstream stalls.
func TestExchangeTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // stall past the client timeout
	}))
	defer srv.Close()
	defer close(release)

	c, err := doh.New(srv.URL+"/dns-query", "", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	start := time.Now()
	if _, err := c.Exchange(context.Background(), q); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout not enforced promptly: %v", elapsed)
	}
}

// TestExchangeNon200 covers the non-200 status branch.
func TestExchangeNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream sad", http.StatusBadGateway)
	}))
	defer srv.Close()

	c, err := doh.New(srv.URL+"/dns-query", "", 5*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, err := c.Exchange(context.Background(), q); err == nil {
		t.Fatal("expected error on non-200 response")
	}
}

// TestExchangeMalformedResponse covers the unpack-failure branch.
func TestExchangeMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write([]byte("not a dns message"))
	}))
	defer srv.Close()

	c, err := doh.New(srv.URL+"/dns-query", "", 5*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if _, err := c.Exchange(context.Background(), q); err == nil {
		t.Fatal("expected unpack error on malformed body")
	}
}
