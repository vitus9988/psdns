//go:build live

package proxy_test

import (
	"context"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/doh"
	"github.com/vitus9988/psdns/internal/resolver"
)

// TestLiveDoHResolve hits the real default DoH endpoint (1.1.1.1) to catch
// real-world regressions the httptest-backed unit tests cannot — TLS changes,
// endpoint moves, wire-format quirks. It needs network access and is excluded
// from the default build (the CI sandbox has no network); run it manually:
//
//	go test -tags live ./internal/proxy/
func TestLiveDoHResolve(t *testing.T) {
	c, err := doh.New("https://1.1.1.1/dns-query", "", 10*time.Second)
	if err != nil {
		t.Fatalf("doh.New: %v", err)
	}
	res := resolver.New(c)

	ips, err := res.Resolve(context.Background(), "one.one.one.one")
	if err != nil {
		t.Fatalf("live Resolve: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("live Resolve returned no addresses")
	}
	t.Logf("resolved one.one.one.one -> %v", ips)
}
