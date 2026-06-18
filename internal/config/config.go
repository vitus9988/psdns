// Package config holds psdns runtime configuration shared across the
// DNS resolver and the SNI-bypass proxy.
package config

import "time"

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

// Config is the resolved runtime configuration.
type Config struct {
	DoHURL       string        // upstream DoH endpoint (RFC 8484)
	DoHBootstrap string        // optional IP[:port] dialed for the DoH host (bypass system DNS)
	DNSListen    string        // local DNS server listen address
	ProxyListen  string        // HTTP CONNECT proxy listen address
	SocksListen  string        // SOCKS5 proxy listen address
	Frag         FragStrategy  // ClientHello fragmentation strategy
	FragDelay    time.Duration // optional delay inserted between fragments
	Timeout      time.Duration // dial / query timeout
}

// Default returns the baseline configuration. The default DoH endpoint uses an
// IP-literal host so the resolver connection sends no SNI of its own and needs
// no DNS bootstrap.
func Default() Config {
	return Config{
		DoHURL:      "https://1.1.1.1/dns-query",
		DNSListen:   "127.0.0.1:53",
		ProxyListen: "127.0.0.1:8080",
		SocksListen: "127.0.0.1:1080",
		Frag:        FragSplit,
		FragDelay:   0,
		Timeout:     10 * time.Second,
	}
}
