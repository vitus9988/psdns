package sysproxy

import (
	"net"
	"strconv"
	"strings"
)

// This file holds the OS-specific proxy-setting formatters and parsers. They are
// pure (no exec/registry side effects) and live here, not in the build-tagged OS
// files, so every formatter/parser is compiled and unit-tested on every OS.

// --- Windows (WinINET registry values) ---

// formatProxyServer builds the WinINET ProxyServer value, pointing both http and
// https at the same proxy: "http=h:p;https=h:p". psdns also forwards plaintext
// http, so the http entry is valid too.
func formatProxyServer(host string, port int) string {
	hp := net.JoinHostPort(host, strconv.Itoa(port))
	return "http=" + hp + ";https=" + hp
}

// formatProxyOverride builds the WinINET ProxyOverride value from a bypass list,
// always appending "<local>" (bypass for hostnames without a dot).
func formatProxyOverride(bypass []string) string {
	parts := append([]string{}, bypass...)
	parts = append(parts, "<local>")
	return strings.Join(parts, ";")
}

// parseWinINETProxyServer extracts host:port from a WinINET ProxyServer value —
// the reverse of formatProxyServer. It accepts the per-protocol form
// ("http=h:p;https=h:p", preferring the https entry) and the bare "h:p" form that
// applies to every protocol. ok is false when no host:port can be parsed.
func parseWinINETProxyServer(v string) (host string, port int, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", 0, false
	}
	if !strings.Contains(v, "=") {
		return splitHostPort(v) // bare "h:p" for all protocols
	}
	// Per-protocol list: prefer https, then http, then the first parsable entry.
	byProto := map[string]string{}
	var firstHP string
	for _, part := range strings.Split(v, ";") {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		proto := strings.ToLower(strings.TrimSpace(part[:eq]))
		hp := strings.TrimSpace(part[eq+1:])
		byProto[proto] = hp
		if firstHP == "" {
			firstHP = hp
		}
	}
	for _, proto := range []string{"https", "http"} {
		if hp, exists := byProto[proto]; exists {
			if h, p, valid := splitHostPort(hp); valid {
				return h, p, true
			}
		}
	}
	return splitHostPort(firstHP)
}

// splitHostPort parses "host:port" into its parts, requiring a valid port.
func splitHostPort(hp string) (host string, port int, ok bool) {
	h, portStr, err := net.SplitHostPort(strings.TrimSpace(hp))
	if err != nil {
		return "", 0, false
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return "", 0, false
	}
	return h, p, true
}

// isLoopback reports whether host is a loopback address or the "localhost" name.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// currentFromBackup pulls the active web proxy out of a captured Backup, so the
// GUI can detect a conflicting local proxy before overwriting it. Only the field
// matching the current OS is populated. The https/secure entry is preferred over
// plain http. Returns a disabled (empty) result when no proxy is active.
func currentFromBackup(b Backup) DetectedProxy {
	switch {
	case b.Windows != nil:
		if b.Windows.ProxyEnable != 1 {
			return DetectedProxy{}
		}
		if h, p, ok := parseWinINETProxyServer(b.Windows.ProxyServer); ok {
			return DetectedProxy{Enabled: true, Host: h, Port: p}
		}
	case b.Darwin != nil:
		for _, s := range b.Darwin.Services {
			if s.SecureEnabled && s.SecureServer != "" && s.SecurePort > 0 {
				return DetectedProxy{Enabled: true, Host: s.SecureServer, Port: s.SecurePort}
			}
			if s.WebEnabled && s.WebServer != "" && s.WebPort > 0 {
				return DetectedProxy{Enabled: true, Host: s.WebServer, Port: s.WebPort}
			}
		}
	case b.Linux != nil:
		if b.Linux.Mode == "manual" {
			if b.Linux.HTTPSHost != "" && b.Linux.HTTPSPort > 0 {
				return DetectedProxy{Enabled: true, Host: b.Linux.HTTPSHost, Port: b.Linux.HTTPSPort}
			}
			if b.Linux.HTTPHost != "" && b.Linux.HTTPPort > 0 {
				return DetectedProxy{Enabled: true, Host: b.Linux.HTTPHost, Port: b.Linux.HTTPPort}
			}
		}
	}
	return DetectedProxy{}
}

// matchesApplied reports whether the OS proxy cur is still exactly what a
// crashed psdns session recorded as applied (all loopback spellings count as
// equal, same port) — i.e. nothing else has touched the setting since.
func matchesApplied(cur DetectedProxy, applied string) bool {
	h, p, ok := splitHostPort(applied)
	if !ok || !cur.Enabled || cur.Port != p {
		return false
	}
	if isLoopback(cur.Host) && isLoopback(h) {
		return true
	}
	return strings.EqualFold(cur.Host, h)
}

// staleEntry reports whether a captured proxy entry must not be restored as-is:
// a loopback entry that is either the address Apply is about to serve (a remnant
// of a previous psdns run — our own listener answers a probe, so liveness alone
// could never clear it) or one nobody is listening on. Restoring either would
// point the OS at a dead proxy and cut the network. Non-loopback entries (e.g. a
// corporate proxy) are never stale and are never probed.
func staleEntry(host string, port int, ours Settings, alive func(host string, port int) bool) bool {
	if !isLoopback(host) {
		return false
	}
	if port == ours.Port && isLoopback(ours.Host) {
		return true
	}
	return !alive(host, port)
}

// neutralizeStale returns b with every stale proxy entry (see staleEntry)
// flipped to disabled, so Restore/RecoverStale can only ever bring back a state
// that works without us: a live third-party proxy is kept exactly as captured,
// a dead leftover becomes "proxy off". The input backup is not mutated.
func neutralizeStale(b Backup, ours Settings, alive func(host string, port int) bool) Backup {
	switch {
	case b.Windows != nil:
		w := *b.Windows
		if w.ProxyEnable == 1 {
			if h, p, ok := parseWinINETProxyServer(w.ProxyServer); ok && staleEntry(h, p, ours, alive) {
				w.ProxyEnable = 0
			}
		}
		b.Windows = &w
	case b.Darwin != nil:
		d := darwinBackup{Services: append([]darwinService(nil), b.Darwin.Services...)}
		for i, svc := range d.Services {
			if svc.WebEnabled && staleEntry(svc.WebServer, svc.WebPort, ours, alive) {
				d.Services[i].WebEnabled = false
			}
			if svc.SecureEnabled && staleEntry(svc.SecureServer, svc.SecurePort, ours, alive) {
				d.Services[i].SecureEnabled = false
			}
		}
		b.Darwin = &d
	case b.Linux != nil:
		l := *b.Linux
		if l.Mode == "manual" {
			// gsettings is one global setting; when the entry it would restore is
			// stale, the only sane restore target is "no proxy".
			if cur := currentFromBackup(b); cur.Enabled && staleEntry(cur.Host, cur.Port, ours, alive) {
				l.Mode = "none"
			}
		}
		b.Linux = &l
	}
	return b
}

// --- Linux (GNOME gsettings) ---

// formatIgnoreHosts renders a bypass list as a GVariant string-array literal:
// ['localhost','127.0.0.1',...]. An empty list yields "[]".
func formatIgnoreHosts(bypass []string) string {
	quoted := make([]string, len(bypass))
	for i, h := range bypass {
		quoted[i] = "'" + h + "'"
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// parseGsettingsValue trims the surrounding quotes/whitespace from a scalar
// `gsettings get` result (e.g. "'manual'\n" -> "manual"; "8080\n" -> "8080").
func parseGsettingsValue(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(s, "'")
	s = strings.TrimSuffix(s, "'")
	return s
}

// --- macOS (networksetup) ---

// parseNetworkServices parses `networksetup -listallnetworkservices`: the first
// line is an informational header and a leading '*' marks a disabled service,
// both skipped. Returns the enabled service names (which may contain spaces).
func parseNetworkServices(b []byte) []string {
	lines := strings.Split(string(b), "\n")
	var out []string
	for i, ln := range lines {
		ln = strings.TrimRight(ln, "\r")
		if i == 0 || ln == "" { // header line, or blank
			continue
		}
		if strings.HasPrefix(ln, "*") { // disabled service
			continue
		}
		out = append(out, ln)
	}
	return out
}

// webProxyState is the parsed result of `networksetup -getwebproxy <svc>`.
type webProxyState struct {
	Enabled bool
	Server  string
	Port    int
}

// parseGetWebProxy parses `networksetup -getwebproxy`/`-getsecurewebproxy` output
// (lines "Enabled: Yes|No", "Server: <h>", "Port: <n>").
func parseGetWebProxy(b []byte) webProxyState {
	var st webProxyState
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(ln, "Enabled:"):
			st.Enabled = strings.TrimSpace(strings.TrimPrefix(ln, "Enabled:")) == "Yes"
		case strings.HasPrefix(ln, "Server:"):
			st.Server = strings.TrimSpace(strings.TrimPrefix(ln, "Server:"))
		case strings.HasPrefix(ln, "Port:"):
			st.Port, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(ln, "Port:")))
		}
	}
	return st
}

// parseProxyBypassDomains parses `networksetup -getproxybypassdomains <svc>`:
// one domain per line, or an informational "There aren't any..." line when none
// are set (which yields nil).
func parseProxyBypassDomains(b []byte) []string {
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "There aren't") {
			return nil
		}
		out = append(out, ln)
	}
	return out
}
