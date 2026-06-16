package supervisor

// Mode selects which servers Start brings up. It mirrors the CLI subcommands
// (psdns proxy / resolve / run).
type Mode string

const (
	// ModeProxy runs the HTTP CONNECT and SOCKS5 proxies (no privilege needed).
	ModeProxy Mode = "proxy"
	// ModeResolve runs the local DoH DNS server (binding :53 needs privilege).
	ModeResolve Mode = "resolve"
	// ModeRun runs the resolver and proxies together.
	ModeRun Mode = "run"
)

// Valid reports whether m is one of the known modes.
func (m Mode) Valid() bool {
	switch m {
	case ModeProxy, ModeResolve, ModeRun:
		return true
	default:
		return false
	}
}

// Listener kinds, used as the stable keys for per-server status.
const (
	KindDNS   = "dns"
	KindHTTP  = "http"
	KindSOCKS = "socks"
)
