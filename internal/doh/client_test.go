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
