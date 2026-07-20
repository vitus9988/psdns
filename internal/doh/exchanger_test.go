package doh_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
)

// answeringServer is a minimal DoH upstream that echoes a valid reply for every
// query, for tests that only care about construction/wiring, not answers.
func answeringServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		resp := new(dns.Msg)
		resp.SetReply(q)
		packed, _ := resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(packed)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestNewExchangerPrimaryOnly verifies that without fallbacks the exchanger is
// the bare client (no hedging wrapper), and that it exchanges successfully.
func TestNewExchangerPrimaryOnly(t *testing.T) {
	srv := answeringServer(t)
	ex, err := doh.NewExchanger(srv.URL+"/dns-query", "", nil, 5*time.Second, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("NewExchanger: %v", err)
	}
	if _, ok := ex.(*doh.Client); !ok {
		t.Fatalf("single upstream should be a bare *Client, got %T", ex)
	}
	if _, err := ex.Exchange(context.Background(), testQuery()); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
}

// TestNewExchangerWithFallbacks verifies fallback endpoints produce a hedged
// exchanger that still answers via the primary.
func TestNewExchangerWithFallbacks(t *testing.T) {
	primary := answeringServer(t)
	fallback := answeringServer(t)
	ex, err := doh.NewExchanger(primary.URL+"/dns-query", "", []string{fallback.URL + "/dns-query"}, 5*time.Second, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("NewExchanger: %v", err)
	}
	if _, ok := ex.(*doh.Client); ok {
		t.Fatal("exchanger with fallbacks should be hedged, got a bare *Client")
	}
	if _, err := ex.Exchange(context.Background(), testQuery()); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
}

func TestNewExchangerBadPrimary(t *testing.T) {
	if _, err := doh.NewExchanger("ftp://1.1.1.1/dns-query", "", nil, time.Second, 0); err == nil {
		t.Fatal("expected error for non-http(s) primary endpoint")
	}
}

func TestNewExchangerBadFallback(t *testing.T) {
	srv := answeringServer(t)
	_, err := doh.NewExchanger(srv.URL+"/dns-query", "", []string{"ftp://8.8.8.8/dns-query"}, time.Second, 0)
	if err == nil {
		t.Fatal("expected error for non-http(s) fallback endpoint")
	}
	if !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("error %q should identify the failing fallback", err)
	}
}

func TestNewInvalidURL(t *testing.T) {
	if _, err := doh.New("http://[::1", "", time.Second); err == nil {
		t.Fatal("expected parse error for malformed endpoint URL")
	}
}

// TestNewDefaultsHTTPPort80 covers the http-scheme default port branch; the
// client is only constructed, never dialed.
func TestNewDefaultsHTTPPort80(t *testing.T) {
	if _, err := doh.New("http://192.0.2.1/dns-query", "", time.Second); err != nil {
		t.Fatalf("New: %v", err)
	}
}

// TestNewBootstrapBareIP covers the bootstrap-without-port branch (a bare IP
// inherits the endpoint's port).
func TestNewBootstrapBareIP(t *testing.T) {
	if _, err := doh.New("https://192.0.2.1/dns-query", "192.0.2.2", time.Second); err != nil {
		t.Fatalf("New: %v", err)
	}
}

// TestNewHedgedSinglePassthrough verifies NewHedged unwraps a single upstream.
func TestNewHedgedSinglePassthrough(t *testing.T) {
	c, err := doh.New("https://192.0.2.1/dns-query", "", time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := doh.NewHedged([]doh.Exchanger{c}, time.Second); got != doh.Exchanger(c) {
		t.Fatalf("single-upstream NewHedged should return the upstream itself, got %T", got)
	}
}

// TestHedgedNoUpstreams covers the defensive empty-upstreams error.
func TestHedgedNoUpstreams(t *testing.T) {
	h := doh.NewHedged(nil, 0)
	if _, err := h.Exchange(context.Background(), testQuery()); err == nil {
		t.Fatal("expected error for hedged exchanger with no upstreams")
	}
}

// TestExchangePackError covers the query-pack failure branch: a label longer
// than 63 octets cannot be packed.
func TestExchangePackError(t *testing.T) {
	c, err := doh.New("https://192.0.2.1/dns-query", "", time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q := new(dns.Msg)
	q.SetQuestion(strings.Repeat("a", 70)+".", dns.TypeA)
	if _, err := c.Exchange(context.Background(), q); err == nil {
		t.Fatal("expected pack error for oversized label")
	}
}

// TestExchangeBodyReadError covers the response-body read failure branch: the
// server declares a larger Content-Length than it writes, so the read is cut
// short mid-body.
func TestExchangeBodyReadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()

	c, err := doh.New(srv.URL+"/dns-query", "", 5*time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Exchange(context.Background(), testQuery()); err == nil {
		t.Fatal("expected read error for truncated response body")
	}
}
