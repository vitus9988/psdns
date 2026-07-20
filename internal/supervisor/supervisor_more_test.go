package supervisor

import (
	"net"
	"testing"
	"time"
)

// TestStartRejectsBadDoHURL verifies a config error (bad DoH endpoint scheme)
// fails Start synchronously and leaves the supervisor stopped.
func TestStartRejectsBadDoHURL(t *testing.T) {
	c := freeProxyConfig()
	c.DoHURL = "ftp://1.1.1.1/dns-query"
	sup := New(c)
	if err := sup.Start(ModeProxy); err == nil {
		t.Fatal("Start with bad DoH URL should fail")
	}
	if sup.Status().Running {
		t.Fatal("supervisor should not be running after failed Start")
	}
}

// TestStartStopResolve brings up the DNS server on ephemeral ports and tears it
// down, covering the resolve mode and the DNS branch of Stop.
func TestStartStopResolve(t *testing.T) {
	c := freeProxyConfig()
	c.DNSListen = "127.0.0.1:0"
	sup := New(c)
	if err := sup.Start(ModeResolve); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := sup.WaitSettled(150 * time.Millisecond)
	if !st.Running || st.Mode != ModeResolve {
		t.Fatalf("expected running resolve, got running=%v mode=%q", st.Running, st.Mode)
	}
	l, ok := findListener(st, KindDNS)
	if !ok {
		t.Fatal("dns listener missing")
	}
	if !l.Up || l.Err != "" {
		t.Fatalf("dns listener: up=%v err=%q", l.Up, l.Err)
	}
	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if sup.Status().Running {
		t.Fatal("still running after Stop")
	}
}

// TestStartStopRun brings up all three servers at once (run mode).
func TestStartStopRun(t *testing.T) {
	c := freeProxyConfig()
	c.DNSListen = "127.0.0.1:0"
	sup := New(c)
	if err := sup.Start(ModeRun); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := sup.WaitSettled(150 * time.Millisecond)
	for _, kind := range []string{KindDNS, KindHTTP, KindSOCKS} {
		l, ok := findListener(st, kind)
		if !ok {
			t.Fatalf("listener %q missing", kind)
		}
		if !l.Up || l.Err != "" {
			t.Fatalf("listener %q: up=%v err=%q", kind, l.Up, l.Err)
		}
	}
	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestResolveDNSBindFailureSurfacesErr occupies the UDP port the DNS server
// wants, so its (fixed-port, no-fallback) bind fails asynchronously and must be
// reported through the listener's Err instead of Start's return value.
func TestResolveDNSBindFailureSurfacesErr(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind udp: %v", err)
	}
	defer func() { _ = pc.Close() }()

	c := freeProxyConfig()
	c.DNSListen = pc.LocalAddr().String() // already in use
	sup := New(c)
	if err := sup.Start(ModeResolve); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = sup.Stop() }()

	st := sup.WaitSettled(300 * time.Millisecond)
	l, ok := findListener(st, KindDNS)
	if !ok {
		t.Fatal("dns listener missing")
	}
	if l.Up {
		t.Fatal("dns listener should be down after bind failure")
	}
	if l.Err == "" {
		t.Fatal("dns listener should carry a friendly bind error")
	}
}

// TestProxyBindFailureSurfacesErr points the HTTP proxy at an unbindable
// (non-local) address so every fallback candidate fails; the listener must be
// recorded down with a friendly error while the SOCKS listener still comes up.
func TestProxyBindFailureSurfacesErr(t *testing.T) {
	c := freeProxyConfig()
	c.ProxyListen = "203.0.113.1:1" // TEST-NET-3, not a local interface
	sup := New(c)
	if err := sup.Start(ModeProxy); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = sup.Stop() }()

	st := sup.WaitSettled(150 * time.Millisecond)
	l, ok := findListener(st, KindHTTP)
	if !ok {
		t.Fatal("http listener missing")
	}
	if l.Up || l.Err == "" {
		t.Fatalf("http listener should be down with an error: up=%v err=%q", l.Up, l.Err)
	}
	s, ok := findListener(st, KindSOCKS)
	if !ok || !s.Up {
		t.Fatalf("socks listener should still be up: ok=%v up=%v err=%q", ok, s.Up, s.Err)
	}
}
