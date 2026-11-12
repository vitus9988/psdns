package supervisor

import "net"

// listenTCPFallback binds addr, falling back once to an OS-assigned free port.
// The GUI uses it so a busy or OS-reserved default port — common on Windows,
// where Hyper-V/WSL/Docker reserve TCP ranges (netsh excludedportrange) that
// often include 8080 — doesn't leave the user unable to start. The actual bound
// address is read from the returned listener's Addr.
func listenTCPFallback(addr string) (net.Listener, error) {
	ln, firstErr := net.Listen("tcp", addr)
	if firstErr == nil {
		return ln, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, firstErr
	}
	ln, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, firstErr
	}
	return ln, nil
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
