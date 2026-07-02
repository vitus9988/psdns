package supervisor

import (
	"net"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
)

// freeProxyConfig returns a config whose proxy listeners use ephemeral ports so
// Start binds successfully without needing privileges or fixed ports.
func freeProxyConfig() config.Config {
	c := config.Default()
	c.ProxyListen = "127.0.0.1:0"
	c.SocksListen = "127.0.0.1:0"
	return c
}

func findListener(st State, kind string) (Listener, bool) {
	for _, l := range st.Listeners {
		if l.Kind == kind {
			return l, true
		}
	}
	return Listener{}, false
}

func TestStartStopProxy(t *testing.T) {
	sup := New(freeProxyConfig())
	if err := sup.Start(ModeProxy); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := sup.WaitSettled(150 * time.Millisecond)
	if !st.Running || st.Mode != ModeProxy {
		t.Fatalf("expected running proxy, got running=%v mode=%q", st.Running, st.Mode)
	}
	for _, kind := range []string{KindHTTP, KindSOCKS} {
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
	if sup.Status().Running {
		t.Fatal("still running after Stop")
	}
}

func TestStartTwiceRejected(t *testing.T) {
	sup := New(freeProxyConfig())
	if err := sup.Start(ModeProxy); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = sup.Stop() }()
	if err := sup.Start(ModeProxy); err != ErrAlreadyRunning {
		t.Fatalf("second Start: want ErrAlreadyRunning, got %v", err)
	}
}

func TestStopWhenNotRunning(t *testing.T) {
	sup := New(freeProxyConfig())
	if err := sup.Stop(); err != ErrNotRunning {
		t.Fatalf("Stop: want ErrNotRunning, got %v", err)
	}
}

func TestInvalidMode(t *testing.T) {
	sup := New(freeProxyConfig())
	if err := sup.Start(Mode("bogus")); err != ErrInvalidMode {
		t.Fatalf("Start bogus: want ErrInvalidMode, got %v", err)
	}
}

// TestRestartUsesFreshInstances proves Stop→Start works: the underlying proxies
// are one-shot, so a reused instance would fail the second ListenAndServe with
// net.ErrClosed (listener Up=false). Both starts must report Up listeners.
func TestRestartUsesFreshInstances(t *testing.T) {
	sup := New(freeProxyConfig())
	for i := 0; i < 2; i++ {
		if err := sup.Start(ModeProxy); err != nil {
			t.Fatalf("Start #%d: %v", i, err)
		}
		st := sup.WaitSettled(150 * time.Millisecond)
		l, ok := findListener(st, KindHTTP)
		if !ok || !l.Up || l.Err != "" {
			t.Fatalf("Start #%d: http listener up=%v err=%q ok=%v", i, l.Up, l.Err, ok)
		}
		if err := sup.Stop(); err != nil {
			t.Fatalf("Stop #%d: %v", i, err)
		}
	}
}

// TestBindFallbackWhenOccupied occupies a port, then points the HTTP proxy at it
// so the configured bind fails (EADDRINUSE). The GUI supervisor must fall back
// to a different free port — reporting an Up listener flagged Fallback with a
// bound address that differs from the occupied one — rather than failing.
func TestBindFallbackWhenOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	defer func() { _ = occupied.Close() }()

	c := config.Default()
	c.ProxyListen = occupied.Addr().String() // already in use
	c.SocksListen = "127.0.0.1:0"            // free
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
	if !l.Up || l.Err != "" {
		t.Fatalf("http listener should fall back and be up: up=%v err=%q", l.Up, l.Err)
	}
	if l.Addr == occupied.Addr().String() {
		t.Fatalf("expected fallback to a different port, still bound to occupied %s", l.Addr)
	}
	if !l.Fallback {
		t.Fatalf("expected Fallback=true when the configured port was unusable, addr=%s", l.Addr)
	}
	// The fallback address must actually be accepting connections.
	conn, err := net.DialTimeout("tcp", l.Addr, time.Second)
	if err != nil {
		t.Fatalf("dial fallback %s: %v", l.Addr, err)
	}
	_ = conn.Close()
}

func TestSetConfigRejectedWhileRunning(t *testing.T) {
	sup := New(freeProxyConfig())
	if err := sup.Start(ModeProxy); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = sup.Stop() }()
	if err := sup.SetConfig(config.Default()); err != ErrAlreadyRunning {
		t.Fatalf("SetConfig while running: want ErrAlreadyRunning, got %v", err)
	}
}

func TestSetConfigAppliesWhenStopped(t *testing.T) {
	sup := New(config.Default())
	c := config.Default()
	c.Frag = config.FragTLSRecord
	c.DoHURL = "https://9.9.9.9/dns-query"
	if err := sup.SetConfig(c); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	got := sup.Config()
	if got.Frag != config.FragTLSRecord || got.DoHURL != "https://9.9.9.9/dns-query" {
		t.Fatalf("config not applied: %+v", got)
	}
}
