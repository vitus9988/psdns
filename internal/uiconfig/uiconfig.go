// Package uiconfig converts between the runtime config.Config and the shape the
// GUI frontend works with. It deliberately imports no Wails code so the
// conversion/validation logic is unit-testable without the desktop toolchain.
package uiconfig

import (
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/doh"
)

// Config mirrors config.Config for the frontend. The two durations are carried
// as human strings ("10s", "10ms") because Wails/JSON marshals time.Duration as
// a raw int64 nanosecond count, which is unusable in the UI.
type Config struct {
	DoHURL        string `json:"dohUrl"`
	DoHBootstrap  string `json:"dohBootstrap"`
	DoHFallbacks  string `json:"dohFallbacks"`
	DoHHedgeDelay string `json:"dohHedgeDelay"`
	DNSListen     string `json:"dnsListen"`
	ProxyListen   string `json:"proxyListen"`
	SocksListen   string `json:"socksListen"`
	Frag          string `json:"frag"` // none | split | tls-record
	FragDelay     string `json:"fragDelay"`
	Timeout       string `json:"timeout"`

	// SetSystemProxy mirrors config.SetSystemProxy (GUI-only auto system proxy).
	SetSystemProxy bool `json:"setSystemProxy"`
	// ProxyBypass is config.ProxyBypass as one comma-separated string.
	ProxyBypass string `json:"proxyBypass"`
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
		DoHURL:        c.DoHURL,
		DoHBootstrap:  c.DoHBootstrap,
		DoHFallbacks:  config.FormatDoHList(c.DoHFallbacks),
		DoHHedgeDelay: c.DoHHedgeDelay.String(),
		DNSListen:     c.DNSListen,
		ProxyListen:   c.ProxyListen,
		SocksListen:   c.SocksListen,
		Frag:          string(c.Frag),
		FragDelay:     c.FragDelay.String(),
		Timeout:       c.Timeout.String(),

		SetSystemProxy: c.SetSystemProxy,
		ProxyBypass:    strings.Join(c.ProxyBypass, ", "),
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
	c.DoHFallbacks = config.ParseDoHList(u.DoHFallbacks)
	if u.DNSListen != "" {
		c.DNSListen = u.DNSListen
	}
	if u.ProxyListen != "" {
		c.ProxyListen = u.ProxyListen
	}
	if u.SocksListen != "" {
		c.SocksListen = u.SocksListen
	}

	// SetSystemProxy is a plain bool with no validation; carry it through.
	c.SetSystemProxy = u.SetSystemProxy
	bypass, err := ParseBypass(u.ProxyBypass)
	if err != nil {
		return c, warns, err
	}
	c.ProxyBypass = bypass

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
	if u.DoHHedgeDelay != "" {
		d, err := time.ParseDuration(u.DoHHedgeDelay)
		if err != nil {
			return c, warns, fmt.Errorf("DoH 폴백 지연 형식이 올바르지 않아요: %q (예: 250ms)", u.DoHHedgeDelay)
		}
		if d < 0 || d > config.MaxDoHHedgeDelay {
			return c, warns, fmt.Errorf("DoH 폴백 지연이 범위를 벗어났어요: %q (0 ~ %v)", u.DoHHedgeDelay, config.MaxDoHHedgeDelay)
		}
		c.DoHHedgeDelay = d
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
	// the point of bypassing system DNS.
	if err := config.ValidateBootstrap(c.DoHBootstrap); err != nil {
		return c, warns, fmt.Errorf("부트스트랩 주소는 IP여야 해요: %q (예: 1.1.1.1 또는 1.1.1.1:853)", c.DoHBootstrap)
	}

	// Validate the DoH endpoint by actually constructing a client (no network).
	if _, err := doh.NewExchanger(c.DoHURL, c.DoHBootstrap, c.DoHFallbacks, c.Timeout, c.DoHHedgeDelay); err != nil {
		return c, warns, fmt.Errorf("DoH 주소가 올바르지 않아요: %v", err)
	}

	if _, port, err := net.SplitHostPort(c.DNSListen); err == nil && port == "53" {
		warns = append(warns, "DNS 주소가 53번 포트라, '시스템 DNS'나 '둘 다' 모드는 관리자 권한이 필요해요. 권한 없이 쓰려면 5353처럼 1024 이상 포트로 바꿔 주세요.")
	}

	return c, warns, nil
}

// ParseBypass turns the user's proxy-exception text into normalized host names.
// Entries are separated by commas, semicolons or whitespace; a pasted URL is cut
// down to its host ("https://www.Bank.co.kr:443/login" -> "www.bank.co.kr") and
// a leading "*." or "." is dropped (subdomains are always covered). This is a
// trust boundary: the hosts are written verbatim into OS proxy settings — the
// Windows ProxyOverride ";" list and a GNOME GVariant '...' literal — so anything
// beyond letters, digits, '-' and '.' is rejected.
func ParseBypass(s string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) }) {
		h := strings.ToLower(f)
		if i := strings.Index(h, "://"); i >= 0 {
			h = h[i+3:]
		}
		h, _, _ = strings.Cut(h, "/")
		h, _, _ = strings.Cut(h, ":")
		h = strings.Trim(strings.TrimPrefix(h, "*"), ".")
		if h == "" || strings.Trim(h, "abcdefghijklmnopqrstuvwxyz0123456789.-") != "" {
			return nil, fmt.Errorf("프록시 예외 사이트 형식이 올바르지 않아요: %q (예: example.com)", f)
		}
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out, nil
}
