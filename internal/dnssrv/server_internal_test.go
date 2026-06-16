package dnssrv

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
)

// fakeRW is a minimal dns.ResponseWriter that captures the written message.
type fakeRW struct {
	msg *dns.Msg
}

func (f *fakeRW) LocalAddr() net.Addr  { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53} }
func (f *fakeRW) RemoteAddr() net.Addr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999} }
func (f *fakeRW) WriteMsg(m *dns.Msg) error {
	f.msg = m
	return nil
}
func (f *fakeRW) Write(b []byte) (int, error) {
	m := new(dns.Msg)
	if err := m.Unpack(b); err != nil {
		return 0, err
	}
	f.msg = m
	return len(b), nil
}
func (f *fakeRW) Close() error        { return nil }
func (f *fakeRW) TsigStatus() error   { return nil }
func (f *fakeRW) TsigTimersOnly(bool) {}
func (f *fakeRW) Hijack()             {}

// dohServing returns a DoH client backed by an httptest server that answers A
// queries with the given IP, or fails with HTTP 500 when fail is true.
func dohServing(t *testing.T, ip string, fail bool) *doh.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil || len(q.Question) == 0 {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		resp := new(dns.Msg)
		resp.SetReply(q)
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP(ip),
		})
		packed, _ := resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(packed)
	}))
	t.Cleanup(srv.Close)
	c, err := doh.New(srv.URL+"/dns-query", "", 5*time.Second)
	if err != nil {
		t.Fatalf("doh.New: %v", err)
	}
	return c
}

// TestHandleForwardsAnswer verifies the handler forwards the query to DoH and
// returns the answer with the request's Id preserved.
func TestHandleForwardsAnswer(t *testing.T) {
	const want = "203.0.113.7"
	s := New(dohServing(t, want, false), "127.0.0.1:0", 5*time.Second)

	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	req.Id = 0xBEEF

	rw := &fakeRW{}
	s.handle(rw, req)

	if rw.msg == nil {
		t.Fatalf("handler wrote no message")
	}
	if rw.msg.Id != req.Id {
		t.Fatalf("response Id = %#x, want %#x", rw.msg.Id, req.Id)
	}
	var got string
	for _, rr := range rw.msg.Answer {
		if a, ok := rr.(*dns.A); ok {
			got = a.A.String()
		}
	}
	if got != want {
		t.Fatalf("answer = %q, want %q", got, want)
	}
}

// TestHandleServfailOnDoHError verifies a DoH failure yields a SERVFAIL reply
// (not a dropped query) so the client gets a definitive response.
func TestHandleServfailOnDoHError(t *testing.T) {
	s := New(dohServing(t, "", true), "127.0.0.1:0", 5*time.Second)

	req := new(dns.Msg)
	req.SetQuestion("fail.example.com.", dns.TypeA)
	req.Id = 0x1234

	rw := &fakeRW{}
	s.handle(rw, req)

	if rw.msg == nil {
		t.Fatalf("handler wrote no message on DoH error")
	}
	if rw.msg.Rcode != dns.RcodeServerFailure {
		t.Fatalf("Rcode = %d, want SERVFAIL(%d)", rw.msg.Rcode, dns.RcodeServerFailure)
	}
	if rw.msg.Id != req.Id {
		t.Fatalf("SERVFAIL Id = %#x, want %#x", rw.msg.Id, req.Id)
	}
}
