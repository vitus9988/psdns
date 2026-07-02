package uiconfig

import (
	"strings"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
)

func TestFromConfigRendersDurations(t *testing.T) {
	c := config.Default()
	c.FragDelay = 10 * time.Millisecond
	u := FromConfig(c)
	if u.FragDelay != "10ms" {
		t.Errorf("FragDelay = %q, want 10ms", u.FragDelay)
	}
	if u.Timeout != "10s" {
		t.Errorf("Timeout = %q, want 10s", u.Timeout)
	}
	if u.DoHHedgeDelay != "250ms" {
		t.Errorf("DoHHedgeDelay = %q, want 250ms", u.DoHHedgeDelay)
	}
	if u.Frag != "split" {
		t.Errorf("Frag = %q, want split", u.Frag)
	}
}

func TestToConfigRoundTrip(t *testing.T) {
	in := FromConfig(config.Default())
	in.DNSListen = "127.0.0.1:5353" // non-privileged, so no :53 warning
	in.Frag = "tls-record"
	in.FragDelay = "10ms"
	in.DoHFallbacks = "https://8.8.8.8/dns-query, https://9.9.9.9/dns-query"
	in.DoHHedgeDelay = "100ms"
	in.Timeout = "5s"
	c, warns, err := in.ToConfig()
	if err != nil {
		t.Fatalf("ToConfig: %v", err)
	}
	if c.Frag != config.FragTLSRecord {
		t.Errorf("Frag = %q", c.Frag)
	}
	if c.FragDelay != 10*time.Millisecond {
		t.Errorf("FragDelay = %v", c.FragDelay)
	}
	if got := config.FormatDoHList(c.DoHFallbacks); got != "https://8.8.8.8/dns-query,https://9.9.9.9/dns-query" {
		t.Errorf("DoHFallbacks = %q", got)
	}
	if c.DoHHedgeDelay != 100*time.Millisecond {
		t.Errorf("DoHHedgeDelay = %v", c.DoHHedgeDelay)
	}
	if c.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings (DNS not :53): %v", warns)
	}
}

// TestToConfigSetSystemProxyRoundTrip pins that the bool survives ToConfig in
// both directions. ToConfig seeds from Default() (true), so the explicit-false
// case is the one that could regress if the assignment were dropped.
func TestToConfigSetSystemProxyRoundTrip(t *testing.T) {
	u := FromConfig(config.Default())
	u.DNSListen = "127.0.0.1:5353" // avoid the :53 warning noise
	if !u.SetSystemProxy {
		t.Fatal("FromConfig(Default()) should carry SetSystemProxy=true")
	}

	u.SetSystemProxy = false
	c, _, err := u.ToConfig()
	if err != nil {
		t.Fatalf("ToConfig: %v", err)
	}
	if c.SetSystemProxy {
		t.Error("explicit SetSystemProxy=false must survive ToConfig, got true")
	}

	u.SetSystemProxy = true
	if c, _, _ = u.ToConfig(); !c.SetSystemProxy {
		t.Error("SetSystemProxy=true should round-trip")
	}
}

func TestToConfigPort53Warns(t *testing.T) {
	in := FromConfig(config.Default()) // DNSListen defaults to 127.0.0.1:53
	_, warns, err := in.ToConfig()
	if err != nil {
		t.Fatalf("ToConfig: %v", err)
	}
	if len(warns) == 0 {
		t.Fatal("expected a :53 privilege warning")
	}
}

func TestToConfigRejectsBadInputs(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*Config)
		match string
	}{
		{"bad frag", func(u *Config) { u.Frag = "magic" }, "분할 방식"},
		{"bad fragDelay", func(u *Config) { u.FragDelay = "soon" }, "조각 사이 지연"},
		{"huge fragDelay", func(u *Config) { u.FragDelay = "1h" }, "조각 사이 지연"},
		{"bad doh hedge delay", func(u *Config) { u.DoHHedgeDelay = "soon" }, "DoH 폴백 지연"},
		{"huge doh hedge delay", func(u *Config) { u.DoHHedgeDelay = "1h" }, "DoH 폴백 지연"},
		{"bad timeout", func(u *Config) { u.Timeout = "later" }, "응답 대기 시간"},
		{"bad listen", func(u *Config) { u.ProxyListen = "not-an-addr" }, "주소 형식"},
		{"bad doh", func(u *Config) { u.DoHURL = "ftp://nope" }, "DoH 주소"},
		{"bad doh fallback", func(u *Config) { u.DoHFallbacks = "https://8.8.8.8/dns-query,ftp://nope" }, "DoH 주소"},
		{"bad bootstrap", func(u *Config) { u.DoHBootstrap = "not-an-ip" }, "부트스트랩"},
		{"bootstrap hostname", func(u *Config) { u.DoHBootstrap = "example.com:853" }, "부트스트랩"},
		{"bootstrap bad port", func(u *Config) { u.DoHBootstrap = "1.1.1.1:bad" }, "부트스트랩"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := FromConfig(config.Default())
			u.DNSListen = "127.0.0.1:5353" // avoid the :53 warning noise
			tc.mut(&u)
			_, _, err := u.ToConfig()
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("error %q missing %q", err.Error(), tc.match)
			}
		})
	}
}

func TestToConfigEmptyFragKeepsDefault(t *testing.T) {
	u := FromConfig(config.Default())
	u.DNSListen = "127.0.0.1:5353"
	u.Frag = ""
	c, _, err := u.ToConfig()
	if err != nil {
		t.Fatalf("ToConfig: %v", err)
	}
	if c.Frag != config.FragSplit {
		t.Errorf("empty frag should keep default split, got %q", c.Frag)
	}
}

func TestToConfigAcceptsValidBootstrap(t *testing.T) {
	for _, bs := range []string{"1.1.1.1", "1.1.1.1:853", "2606:4700:4700::1111", "[2606:4700:4700::1111]:853"} {
		u := FromConfig(config.Default())
		u.DNSListen = "127.0.0.1:5353" // avoid the :53 warning noise
		u.DoHBootstrap = bs
		if _, _, err := u.ToConfig(); err != nil {
			t.Fatalf("valid bootstrap %q rejected: %v", bs, err)
		}
	}
}
