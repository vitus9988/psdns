// Package uiconfig converts between the runtime config.Config and the shape the
// GUI frontend works with. It deliberately imports no Wails code so the
// conversion/validation logic is unit-testable without the desktop toolchain.
package uiconfig

import (
	"fmt"
	"net"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/doh"
)

// Config mirrors config.Config for the frontend. The two durations are carried
// as human strings ("10s", "10ms") because Wails/JSON marshals time.Duration as
// a raw int64 nanosecond count, which is unusable in the UI.
type Config struct {
	DoHURL       string `json:"dohUrl"`
	DoHBootstrap string `json:"dohBootstrap"`
	DNSListen    string `json:"dnsListen"`
	ProxyListen  string `json:"proxyListen"`
	SocksListen  string `json:"socksListen"`
	Frag         string `json:"frag"` // none | split | tls-record
	FragDelay    string `json:"fragDelay"`
	Timeout      string `json:"timeout"`
}

// SetResult is the response of a config update: the applied config plus any
// non-fatal warnings to show inline (e.g. ":53 needs admin").
type SetResult struct {
	Config   Config   `json:"config"`
	Warnings []string `json:"warnings"`
}

// FromConfig renders a runtime config for the UI.
func FromConfig(c config.Config) Config {
	return Config{
		DoHURL:       c.DoHURL,
		DoHBootstrap: c.DoHBootstrap,
		DNSListen:    c.DNSListen,
		ProxyListen:  c.ProxyListen,
		SocksListen:  c.SocksListen,
		Frag:         string(c.Frag),
		FragDelay:    c.FragDelay.String(),
		Timeout:      c.Timeout.String(),
	}
}

// ToConfig validates the UI values and returns a runtime config plus warnings.
// A fatal problem (bad duration, unknown frag, malformed address, bad DoH URL)
// returns an error with a friendly Korean message; warnings are advisory.
func (u Config) ToConfig() (config.Config, []string, error) {
	c := config.Default()
	var warns []string

	if u.DoHURL != "" {
		c.DoHURL = u.DoHURL
	}
	c.DoHBootstrap = u.DoHBootstrap
	if u.DNSListen != "" {
		c.DNSListen = u.DNSListen
	}
	if u.ProxyListen != "" {
		c.ProxyListen = u.ProxyListen
	}
	if u.SocksListen != "" {
		c.SocksListen = u.SocksListen
	}

	switch config.FragStrategy(u.Frag) {
	case config.FragNone, config.FragSplit, config.FragTLSRecord:
		c.Frag = config.FragStrategy(u.Frag)
	case "":
		// keep default
	default:
		return c, warns, fmt.Errorf("알 수 없는 분할 방식이에요: %q (none/split/tls-record 중 하나)", u.Frag)
	}

	if u.FragDelay != "" {
		d, err := time.ParseDuration(u.FragDelay)
		if err != nil {
			return c, warns, fmt.Errorf("조각 사이 지연 형식이 올바르지 않아요: %q (예: 10ms)", u.FragDelay)
		}
		if d < 0 || d > config.MaxFragDelay {
			return c, warns, fmt.Errorf("조각 사이 지연이 범위를 벗어났어요: %q (0 ~ %v)", u.FragDelay, config.MaxFragDelay)
		}
		c.FragDelay = d
	}
	if u.Timeout != "" {
		d, err := time.ParseDuration(u.Timeout)
		if err != nil {
			return c, warns, fmt.Errorf("응답 대기 시간 형식이 올바르지 않아요: %q (예: 10s)", u.Timeout)
		}
		c.Timeout = d
	}

	for _, addr := range []string{c.DNSListen, c.ProxyListen, c.SocksListen} {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return c, warns, fmt.Errorf("주소 형식이 올바르지 않아요: %q (host:port 형태로 입력해 주세요)", addr)
		}
	}

	// The bootstrap, when set, must be an IP (optionally with a port): it is the
	// address actually dialed for the DoH host, so a hostname there would defeat
	// the point of bypassing system DNS. doh.New treats a bad value as a bare
	// host without complaint, so validate it here.
	if c.DoHBootstrap != "" {
		hostPart := c.DoHBootstrap
		if h, _, err := net.SplitHostPort(c.DoHBootstrap); err == nil {
			hostPart = h
		}
		if net.ParseIP(hostPart) == nil {
			return c, warns, fmt.Errorf("부트스트랩 주소는 IP여야 해요: %q (예: 1.1.1.1 또는 1.1.1.1:853)", c.DoHBootstrap)
		}
	}

	// Validate the DoH endpoint by actually constructing a client (no network).
	if _, err := doh.New(c.DoHURL, c.DoHBootstrap, c.Timeout); err != nil {
		return c, warns, fmt.Errorf("DoH 주소가 올바르지 않아요: %v", err)
	}

	if _, port, err := net.SplitHostPort(c.DNSListen); err == nil && port == "53" {
		warns = append(warns, "DNS 주소가 53번 포트라, '시스템 DNS'나 '둘 다' 모드는 관리자 권한이 필요해요. 권한 없이 쓰려면 5353처럼 1024 이상 포트로 바꿔 주세요.")
	}

	return c, warns, nil
}
