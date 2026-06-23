package resolver_test

import (
	"context"
	"testing"

	"github.com/vitus9988/psdns/internal/resolver"
)

// BenchmarkResolveCacheHit measures the steady-state cost of a cache hit — the
// common case once a host has been resolved. One warm-up Resolve populates the
// cache, then every timed iteration is served from it without touching the
// upstream, so this isolates the hot path: IP-literal check, key lowercasing,
// mutex lock and map lookup.
//
//	go test -bench=BenchmarkResolveCacheHit -benchmem ./internal/resolver/
func BenchmarkResolveCacheHit(b *testing.B) {
	c, _ := mockDoH(b, answer{a: []dnsRR{{ip: "1.2.3.4", ttl: 300}}})
	r := resolver.New(c)
	ctx := context.Background()

	if _, err := r.Resolve(ctx, "example.com"); err != nil { // warm the cache
		b.Fatalf("warm-up Resolve: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Resolve(ctx, "example.com"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResolveIPLiteral measures the fast path for an IP-literal host, which
// short-circuits before any cache or DoH work.
func BenchmarkResolveIPLiteral(b *testing.B) {
	c, _ := mockDoH(b, answer{}) // never contacted
	r := resolver.New(c)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Resolve(ctx, "93.184.216.34"); err != nil {
			b.Fatal(err)
		}
	}
}
