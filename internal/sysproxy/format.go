package sysproxy

import (
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
	hp := host + ":" + strconv.Itoa(port)
	return "http=" + hp + ";https=" + hp
}

// formatProxyOverride builds the WinINET ProxyOverride value from a bypass list,
// always appending "<local>" (bypass for hostnames without a dot).
func formatProxyOverride(bypass []string) string {
	parts := append([]string{}, bypass...)
	parts = append(parts, "<local>")
	return strings.Join(parts, ";")
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
