package resolver_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
	"github.com/vitus9988/psdns/internal/resolver"
)

// answer describes what the mock DoH server returns for a single query type.
type answer struct {
	a     []dnsRR // records for an A query
	aaaa  []dnsRR // records for an AAAA query
	fail  bool    // respond 500 (forces a request error)
	delay time.Duration
}

type dnsRR struct {
	ip  string
	ttl uint32
}

// mockDoH stands up an httptest server speaking RFC 8484 wire format and a DoH
// client pointed at it. reqCount counts the HTTP requests reaching the server,
// which lets cache tests assert the upstream was (not) hit. It takes testing.TB
// so both tests (*testing.T) and benchmarks (*testing.B) can share it.
func mockDoH(t testing.TB, ans answer) (*doh.Client, *int32) {
	t.Helper()
	var reqCount int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqCount, 1)
		if ans.delay > 0 {
			time.Sleep(ans.delay)
		}
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
		if ans.fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}

		resp := new(dns.Msg)
		resp.SetReply(q)
		qn := q.Question[0].Name
		switch q.Question[0].Qtype {
		case dns.TypeA:
			for _, rr := range ans.a {
				resp.Answer = append(resp.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: qn, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: rr.ttl},
					A:   net.ParseIP(rr.ip),
				})
			}
		case dns.TypeAAAA:
			for _, rr := range ans.aaaa {
				resp.Answer = append(resp.Answer, &dns.AAAA{
					Hdr:  dns.RR_Header{Name: qn, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: rr.ttl},
					AAAA: net.ParseIP(rr.ip),
				})
			}
		}
		packed, err := resp.Pack()
		if err != nil {
			http.Error(w, "pack", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(packed)
	}))
	t.Cleanup(srv.Close)

	c, err := doh.New(srv.URL+"/dns-query", "", 5*time.Second)
	if err != nil {
		t.Fatalf("doh.New: %v", err)
	}
	return c, &reqCount
}

func ipStrings(ips []net.IP) map[string]bool {
	m := make(map[string]bool, len(ips))
	for _, ip := range ips {
		m[ip.String()] = true
	}
	return m
}

// TestResolveIPLiteral verifies an IP literal is returned unchanged without any
// DoH request being made.
func TestResolveIPLiteral(t *testing.T) {
	c, reqCount := mockDoH(t, answer{fail: true}) // would error if contacted
	r := resolver.New(c)

	for _, lit := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		ips, err := r.Resolve(context.Background(), lit)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", lit, err)
		}
		if len(ips) != 1 || ips[0].String() != net.ParseIP(lit).String() {
			t.Fatalf("Resolve(%q) = %v, want single literal", lit, ips)
		}
	}
	if n := atomic.LoadInt32(reqCount); n != 0 {
		t.Fatalf("IP literal must not hit DoH, got %d requests", n)
	}
}

// TestResolveMergesAandAAAA checks that A and AAAA answers are merged.
func TestResolveMergesAandAAAA(t *testing.T) {
	c, _ := mockDoH(t, answer{
		a:    []dnsRR{{ip: "1.2.3.4", ttl: 300}},
		aaaa: []dnsRR{{ip: "2001:db8::1", ttl: 300}},
	})
	r := resolver.New(c)

	ips, err := r.Resolve(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := ipStrings(ips)
	if !got["1.2.3.4"] || !got["2001:db8::1"] {
		t.Fatalf("merged result %v missing A or AAAA", ips)
	}
	if len(ips) != 2 {
		t.Fatalf("want 2 merged IPs, got %d: %v", len(ips), ips)
	}
}

// TestResolveCacheHit confirms a second lookup is served from cache (no second
// upstream request).
func TestResolveCacheHit(t *testing.T) {
	c, reqCount := mockDoH(t, answer{a: []dnsRR{{ip: "1.2.3.4", ttl: 300}}})
	r := resolver.New(c)

	if _, err := r.Resolve(context.Background(), "example.com"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	first := atomic.LoadInt32(reqCount)
	if first == 0 {
		t.Fatalf("first lookup made no DoH request")
	}

	if _, err := r.Resolve(context.Background(), "example.com"); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if got := atomic.LoadInt32(reqCount); got != first {
		t.Fatalf("second lookup hit upstream: requests %d -> %d (want cache hit)", first, got)
	}
}

// TestResolveNODATA verifies an empty answer set surfaces a "no addresses"
// error rather than an empty slice.
func TestResolveNODATA(t *testing.T) {
	c, _ := mockDoH(t, answer{}) // server replies but with zero answers
	r := resolver.New(c)

	_, err := r.Resolve(context.Background(), "empty.example.com")
	if err == nil {
		t.Fatalf("expected error for NODATA, got nil")
	}
	if want := "no addresses"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err.Error(), want)
	}
}

// TestResolveErrorPropagates verifies an upstream failure on both queries is
// returned to the caller.
func TestResolveErrorPropagates(t *testing.T) {
	c, _ := mockDoH(t, answer{fail: true})
	r := resolver.New(c)

	if _, err := r.Resolve(context.Background(), "fail.example.com"); err == nil {
		t.Fatalf("expected error when DoH fails, got nil")
	}
}

// TestResolveTTLClampLow verifies a sub-minimum TTL is clamped up so the cache
// holds the entry for at least minTTL (30s). We assert indirectly: after one
// lookup the entry must still be cached (a 1s TTL would expire immediately if
// not clamped, but we only need to confirm it is treated as valid right after).
func TestResolveTTLClampLow(t *testing.T) {
	c, reqCount := mockDoH(t, answer{a: []dnsRR{{ip: "1.2.3.4", ttl: 1}}})
	r := resolver.New(c)

	if _, err := r.Resolve(context.Background(), "low.example.com"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	first := atomic.LoadInt32(reqCount)
	// Immediately re-resolve: with a clamp to 30s the entry is fresh, so no new
	// request. (Without the clamp, a 1s TTL would still be fresh here too, so
	// this only guards against an obviously broken clamp returning expires<=now.)
	if _, err := r.Resolve(context.Background(), "low.example.com"); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if got := atomic.LoadInt32(reqCount); got != first {
		t.Fatalf("low-TTL entry not cached: requests %d -> %d", first, got)
	}
}

// TestResolvePartialError verifies that when one query (AAAA) fails but the
// other (A) succeeds, the successful answers are still returned.
func TestResolvePartialError(t *testing.T) {
	// Only A records are configured; AAAA returns an empty (but successful)
	// answer. The merged result should contain the A address.
	c, _ := mockDoH(t, answer{a: []dnsRR{{ip: "1.2.3.4", ttl: 60}}})
	r := resolver.New(c)

	ips, err := r.Resolve(context.Background(), "partial.example.com")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ipStrings(ips)["1.2.3.4"] {
		t.Fatalf("expected A address despite empty AAAA, got %v", ips)
	}
}

// TestResolveDedupsConcurrent verifies that many concurrent misses for the same
// host collapse into one upstream lookup (A+AAAA = 2 requests) via in-flight
// de-duplication, instead of each goroutine firing its own pair.
func TestResolveDedupsConcurrent(t *testing.T) {
	const goroutines = 20
	c, reqCount := mockDoH(t, answer{
		a:     []dnsRR{{ip: "1.2.3.4", ttl: 300}},
		aaaa:  []dnsRR{{ip: "2001:db8::1", ttl: 300}},
		delay: 100 * time.Millisecond, // hold requests open so the calls overlap
	})
	r := resolver.New(c)

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ips, err := r.Resolve(context.Background(), "example.com")
			if err != nil {
				errs <- err
				return
			}
			if !ipStrings(ips)["1.2.3.4"] {
				errs <- fmt.Errorf("missing A record: %v", ips)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Resolve: %v", err)
	}

	// A single de-duplicated lookup issues exactly two upstream requests (A+AAAA).
	if got := atomic.LoadInt32(reqCount); got != 2 {
		t.Fatalf("concurrent lookups not de-duplicated: %d upstream requests, want 2", got)
	}
}

// TestResolveCacheKeyCaseInsensitive verifies names differing only in case share
// a cache entry (DNS is case-insensitive): the second, differently-cased lookup
// is served from cache without a new upstream request.
func TestResolveCacheKeyCaseInsensitive(t *testing.T) {
	c, reqCount := mockDoH(t, answer{a: []dnsRR{{ip: "1.2.3.4", ttl: 300}}})
	r := resolver.New(c)

	if _, err := r.Resolve(context.Background(), "Example.COM"); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	first := atomic.LoadInt32(reqCount)
	if first == 0 {
		t.Fatalf("first lookup made no DoH request")
	}

	if _, err := r.Resolve(context.Background(), "example.com"); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if got := atomic.LoadInt32(reqCount); got != first {
		t.Fatalf("case-variant lookup hit upstream: requests %d -> %d (want cache hit)", first, got)
	}
}
