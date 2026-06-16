package dnssrv_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/dnssrv"
	"github.com/vitus9988/psdns/internal/doh"
)

// freeUDPPort returns a loopback UDP address whose port is currently free. There
// is an unavoidable bind race between releasing it here and the server binding
// it, but on loopback in CI this is reliable; the server starts immediately and
// the test polls before querying.
func freeUDPPort(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}

// TestServerRoundTrip starts the real UDP+TCP server, backs it with a mock DoH,
// and performs a genuine UDP query end-to-end through ListenAndServe.
func TestServerRoundTrip(t *testing.T) {
	const want = "198.51.100.42"

	dohSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil || len(q.Question) == 0 {
			http.Error(w, "bad", http.StatusBadRequest)
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
	defer dohSrv.Close()

	dohClient, err := doh.New(dohSrv.URL+"/dns-query", "", 5*time.Second)
	if err != nil {
		t.Fatalf("doh.New: %v", err)
	}

	addr := freeUDPPort(t)
	srv := dnssrv.New(dohClient, addr, 5*time.Second)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	t.Cleanup(srv.Shutdown)

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	q.Id = 0xABCD
	cl := &dns.Client{Net: "udp", Timeout: 2 * time.Second}

	// Poll until the listener is up and answering.
	var resp *dns.Msg
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, _, err := cl.Exchange(q, addr)
		if err == nil && r != nil {
			resp = r
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp == nil {
		t.Fatalf("no response from server at %s", addr)
	}
	if resp.Id != q.Id {
		t.Fatalf("response Id = %#x, want %#x", resp.Id, q.Id)
	}
	var got string
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok {
			got = a.A.String()
		}
	}
	if got != want {
		t.Fatalf("answer = %q, want %q", got, want)
	}
}
