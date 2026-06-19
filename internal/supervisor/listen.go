package supervisor

import "net"

// portAlternates lists stable fallback ports for the well-known proxy defaults.
// They are tried before an OS-assigned port so the proxy address stays the same
// across runs when the default is busy or OS-reserved, instead of jumping to a
// random ephemeral port the user would have to re-copy into their browser.
var portAlternates = map[string][]string{
	"8080": {"8081", "8088", "18080"}, // HTTP CONNECT default
	"1080": {"1081", "1088", "11080"}, // SOCKS5 default
}

// listenCandidates returns the addresses to try for addr, in order: the
// requested address, then any stable alternates for its port, then host:0 (an
// OS-assigned free port) as a guaranteed last resort. An unparseable addr is
// returned as the single candidate.
func listenCandidates(addr string) []string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return []string{addr}
	}
	cands := []string{addr}
	for _, alt := range portAlternates[port] {
		cands = append(cands, net.JoinHostPort(host, alt))
	}
	return append(cands, net.JoinHostPort(host, "0"))
}

// listenTCPFallback binds the first usable address among listenCandidates(addr).
// The GUI uses it so a busy or OS-reserved default port — common on Windows,
// where Hyper-V/WSL/Docker reserve TCP ranges (netsh excludedportrange) that
// often include 8080 — doesn't leave the user unable to start. The actual bound
// address is read from the returned listener's Addr.
func listenTCPFallback(addr string) (net.Listener, error) {
	var firstErr error
	for _, cand := range listenCandidates(addr) {
		ln, err := net.Listen("tcp", cand)
		if err == nil {
			return ln, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// fellBack reports whether bound ended up on a different port than the explicitly
// requested addr. A "0" (any) request never counts as a fallback.
func fellBack(requested, bound string) bool {
	_, rport, err := net.SplitHostPort(requested)
	if err != nil || rport == "0" || rport == "" {
		return false
	}
	_, bport, err := net.SplitHostPort(bound)
	if err != nil {
		return false
	}
	return rport != bport
}
