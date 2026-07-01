package config_test

import (
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
)

func TestDefault(t *testing.T) {
	c := config.Default()

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"DoHURL", c.DoHURL, "https://1.1.1.1/dns-query"},
		{"DoHBootstrap", c.DoHBootstrap, ""},
		{"DNSListen", c.DNSListen, "127.0.0.1:53"},
		{"ProxyListen", c.ProxyListen, "127.0.0.1:8080"},
		{"SocksListen", c.SocksListen, "127.0.0.1:1080"},
		{"Frag", c.Frag, config.FragSplit},
		{"FragDelay", c.FragDelay, time.Duration(0)},
		{"Timeout", c.Timeout, 10 * time.Second},
		{"SetSystemProxy", c.SetSystemProxy, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("Default().%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestFragStrategyConstants pins the string values of the fragmentation
// strategies, since they are parsed from CLI flags and must stay stable.
func TestFragStrategyConstants(t *testing.T) {
	tests := []struct {
		strat config.FragStrategy
		want  string
	}{
		{config.FragNone, "none"},
		{config.FragSplit, "split"},
		{config.FragTLSRecord, "tls-record"},
	}
	for _, tt := range tests {
		if string(tt.strat) != tt.want {
			t.Errorf("strategy %v = %q, want %q", tt.strat, string(tt.strat), tt.want)
		}
	}
}

func TestValidateBootstrap(t *testing.T) {
	valid := []string{
		"",
		"1.1.1.1",
		"1.1.1.1:853",
		"2606:4700:4700::1111",
		"[2606:4700:4700::1111]:853",
	}
	for _, bootstrap := range valid {
		if err := config.ValidateBootstrap(bootstrap); err != nil {
			t.Errorf("ValidateBootstrap(%q): %v", bootstrap, err)
		}
	}

	invalid := []string{
		"example.com",
		"example.com:853",
		"1.1.1.1:bad",
		"1.1.1.1:99999",
		"[2606:4700:4700::1111]",
	}
	for _, bootstrap := range invalid {
		if err := config.ValidateBootstrap(bootstrap); err == nil {
			t.Errorf("ValidateBootstrap(%q): expected error", bootstrap)
		}
	}
}
