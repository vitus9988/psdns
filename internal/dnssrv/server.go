// Package dnssrv runs a local DNS server that forwards every query upstream
// over DoH. Point the OS resolver at it (e.g. 127.0.0.1) to neutralise ISP
// DNS tampering system-wide.
package dnssrv

import (
	"context"
	"errors"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
)

// drainTimeout bounds how long Shutdown waits for in-flight query handlers to
// finish after the sockets are closed. Generous for a handler that only has to
// notice its write failed, short enough that a quit never looks hung. A var so
// tests can shrink it rather than spend it.
var drainTimeout = 2 * time.Second

// Server listens on UDP and TCP and relays queries to a DoH upstream.
type Server struct {
	doh     doh.Exchanger
	timeout time.Duration
	addr    string
	udp     *dns.Server
	tcp     *dns.Server

	// The bound sockets are kept here (guarded by mu, with closed for
	// idempotence) so Shutdown can close them itself: miekg's Shutdown is a
	// no-op until its serve loop has flagged the server as started, so a
	// Shutdown racing ListenAndServe would otherwise leave the port bound in
	// this process forever.
	mu     sync.Mutex
	pc     net.PacketConn
	ln     net.Listener
	closed bool
}

// New builds a server listening on addr (host:port).
func New(c doh.Exchanger, addr string, timeout time.Duration) *Server {
	s := &Server{doh: c, timeout: timeout, addr: addr}
	h := dns.HandlerFunc(s.handle)
	s.udp = &dns.Server{Addr: addr, Net: "udp", Handler: h}
	s.tcp = &dns.Server{Addr: addr, Net: "tcp", Handler: h}
	return s
}

func (s *Server) handle(w dns.ResponseWriter, req *dns.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	resp, err := s.doh.Exchange(ctx, req)
	if err != nil {
		log.Printf("dns: forward failed: %v", err)
		m := new(dns.Msg)
		m.SetRcode(req, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
		return
	}
	resp.Id = req.Id
	// Over UDP a datagram larger than the client's advertised buffer (or the
	// 512-byte default when it sends no EDNS0 OPT) can be silently dropped in
	// transit, so a large answer would just vanish. Truncate it to fit and set
	// the TC bit, which tells the client to retry the query over TCP (RFC 1035
	// §4.2.1) — where no such size limit applies. TCP responses are never
	// truncated.
	if isUDP(w) {
		resp.Truncate(udpSize(req))
	}
	_ = w.WriteMsg(resp)
}

// isUDP reports whether the response will go back over UDP (as opposed to TCP),
// which is the only transport that needs size-based truncation.
func isUDP(w dns.ResponseWriter) bool {
	if a := w.RemoteAddr(); a != nil {
		return a.Network() == "udp"
	}
	return false
}

// udpSize is the largest UDP payload the client will accept: its EDNS0-advertised
// buffer, floored at the 512-byte default (dns.MinMsgSize) and never taken below
// it even if a client advertises something smaller.
func udpSize(req *dns.Msg) int {
	size := dns.MinMsgSize
	if opt := req.IsEdns0(); opt != nil {
		if adv := int(opt.UDPSize()); adv > size {
			size = adv
		}
	}
	return size
}

// ListenAndServe binds the UDP and TCP listeners and blocks until one of them
// stops. Binding happens synchronously up front so a failure on either protocol
// closes the socket already opened for the other — neither is left orphaned —
// and the bind error is returned directly. Once both are serving, the first one
// to exit triggers Shutdown of the other.
func (s *Server) ListenAndServe() error {
	// Already stopped: don't take the port at all. The post-bind check below is
	// what makes this safe rather than merely likely (a Shutdown can still land
	// between here and the bind), but skipping the bind outright keeps a stopped
	// server from briefly reclaiming a port a new run may be taking.
	s.mu.Lock()
	stopped := s.closed
	s.mu.Unlock()
	if stopped {
		return net.ErrClosed
	}

	pc, err := net.ListenPacket("udp", s.addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		_ = pc.Close()
		return err
	}
	s.mu.Lock()
	if s.closed {
		// Shutdown won the race: release the sockets we just bound and report
		// a clean close instead of serving on our way out.
		s.mu.Unlock()
		_ = pc.Close()
		_ = ln.Close()
		return net.ErrClosed
	}
	s.pc, s.ln = pc, ln
	s.udp.PacketConn = pc
	s.tcp.Listener = ln
	s.mu.Unlock()

	errCh := make(chan error, 2)
	go func() { errCh <- s.udp.ActivateAndServe() }()
	go func() { errCh <- s.tcp.ActivateAndServe() }()
	err = <-errCh
	_ = s.Shutdown()
	return err
}

// Shutdown stops both listeners and releases their ports. It is idempotent and
// safe to call at any point relative to ListenAndServe: the bound sockets are
// closed directly first, because miekg's Shutdown does nothing (and closes
// nothing) before its serve loop has marked the server started — relying on it
// alone would leak the port inside this process when a stop lands in that
// window.
func (s *Server) Shutdown() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pc, ln := s.pc, s.ln
	s.mu.Unlock()

	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil && !errors.Is(err, net.ErrClosed) {
			firstErr = err
		}
	}
	if ln != nil {
		record(ln.Close())
	}
	if pc != nil {
		record(pc.Close())
	}
	// Let miekg unwind its own state and wait out in-flight handlers, but only
	// briefly: its Shutdown blocks until every handler goroutine has returned,
	// and a handler waiting on a slow DoH exchange holds that for the whole query
	// timeout (10s by default). The ports are already released above, so this
	// wait is politeness — letting answers in flight finish — and must not stall
	// a quit, which on macOS runs this teardown on the main thread. "server not
	// started" only means we won the race above and nothing was serving yet.
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	for _, srv := range []*dns.Server{s.udp, s.tcp} {
		if srv == nil {
			continue
		}
		if err := srv.ShutdownContext(ctx); err != nil && !isNotStarted(err) && !errors.Is(err, context.DeadlineExceeded) {
			record(err)
		}
	}
	return firstErr
}

// isNotStarted matches miekg/dns's "server not started" Shutdown error, which
// it exposes only as text (no sentinel as of v1.1.72).
func isNotStarted(err error) bool {
	return err != nil && strings.Contains(err.Error(), "server not started")
}
