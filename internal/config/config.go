// Package config holds psdns runtime configuration shared across the
// DNS resolver and the SNI-bypass proxy.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// FragStrategy selects how the first client→server payload (the TLS
// ClientHello) is written to the upstream so that on-path DPI cannot parse
// the plaintext SNI field.
type FragStrategy string

const (
	// FragNone writes the ClientHello unmodified (no SNI-block bypass).
	FragNone FragStrategy = "none"
	// FragSplit writes the ClientHello as multiple TCP segments, splitting
	// inside the SNI host name when it can be located.
	FragSplit FragStrategy = "split"
	// FragTLSRecord re-frames the ClientHello handshake into multiple TLS
	// records (valid per RFC 8446 §5.1) so a DPI inspecting only the first
	// record sees a truncated SNI.
	FragTLSRecord FragStrategy = "tls-record"
)

// MaxFragDelay caps the per-fragment delay. Real use is microseconds to a few
// tens of milliseconds; a far larger value (e.g. "1h") is almost certainly a
// mistake that would stall every connection, so callers reject it up front.
const MaxFragDelay = 5 * time.Second

// MaxDoHHedgeDelay caps how long psdns waits before trying the next fallback
// DoH endpoint. Large values are usually a configuration mistake because they
// directly add DNS lookup latency when the primary endpoint is blocked.
const MaxDoHHedgeDelay = 5 * time.Second

// Config is the resolved runtime configuration.
type Config struct {
	DoHURL        string        // upstream DoH endpoint (RFC 8484)
	DoHBootstrap  string        // optional IP[:port] dialed for the DoH host (bypass system DNS)
	DoHFallbacks  []string      // optional fallback DoH endpoint URLs, tried with hedging
	DoHHedgeDelay time.Duration // delay before starting each fallback DoH query
	DNSListen     string        // local DNS server listen address
	ProxyListen   string        // HTTP CONNECT proxy listen address
	SocksListen   string        // SOCKS5 proxy listen address
	Frag          FragStrategy  // ClientHello fragmentation strategy
	FragDelay     time.Duration // optional delay inserted between fragments
	Timeout       time.Duration // dial / query timeout

	// SetSystemProxy makes the GUI point the OS web proxy (http+https) at the
	// running HTTP proxy on start and restore it on stop/quit. GUI-only: the CLI
	// and supervisor never read this field, so their behavior is unchanged.
	SetSystemProxy bool
}

// Default returns the baseline configuration. The default DoH endpoint uses an
// IP-literal host so the resolver connection sends no SNI of its own and needs
// no DNS bootstrap.
func Default() Config {
	return Config{
		DoHURL:        "https://1.1.1.1/dns-query",
		DoHHedgeDelay: 250 * time.Millisecond,
		DNSListen:     "127.0.0.1:53",
		ProxyListen:   "127.0.0.1:8080",
		SocksListen:   "127.0.0.1:1080",
		Frag:          FragSplit,
		FragDelay:     0,
		Timeout:       10 * time.Second,

		// Auto-configure the OS web proxy by default so the GUI's one button is
		// enough; the GUI exposes a toggle to turn it off.
		SetSystemProxy: true,
	}
}

// ValidateBootstrap rejects a DoH bootstrap value that would require system DNS.
// A valid bootstrap is an IP literal, optionally with a port. Bare IPv6 literals
// are accepted; IPv6 with a port must use bracket notation.
func ValidateBootstrap(bootstrap string) error {
	if bootstrap == "" {
		return nil
	}
	host := bootstrap
	if h, p, err := net.SplitHostPort(bootstrap); err == nil {
		host = h
		port, err := strconv.Atoi(p)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("invalid bootstrap port in %q", bootstrap)
		}
	}
	if net.ParseIP(host) == nil {
		return fmt.Errorf("bootstrap must be an IP literal: %q", bootstrap)
	}
	return nil
}

// ParseDoHList parses a comma-separated list of DoH endpoint URLs, trimming
// whitespace and ignoring empty entries.
func ParseDoHList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// FormatDoHList renders a DoH endpoint list in the same comma-separated form
// accepted by ParseDoHList.
func FormatDoHList(urls []string) string {
	return strings.Join(urls, ",")
}
