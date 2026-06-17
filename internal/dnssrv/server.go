// Package dnssrv runs a local DNS server that forwards every query upstream
// over DoH. Point the OS resolver at it (e.g. 127.0.0.1) to neutralise ISP
// DNS tampering system-wide.
package dnssrv

import (
	"context"
	"log"
	"net"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
)

// Server listens on UDP and TCP and relays queries to a DoH upstream.
type Server struct {
	doh     *doh.Client
	timeout time.Duration
	addr    string
	udp     *dns.Server
	tcp     *dns.Server
}

// New builds a server listening on addr (host:port).
func New(c *doh.Client, addr string, timeout time.Duration) *Server {
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
	_ = w.WriteMsg(resp)
}

// ListenAndServe binds the UDP and TCP listeners and blocks until one of them
// stops. Binding happens synchronously up front so a failure on either protocol
// closes the socket already opened for the other — neither is left orphaned —
// and the bind error is returned directly. Once both are serving, the first one
// to exit triggers Shutdown of the other.
func (s *Server) ListenAndServe() error {
	pc, err := net.ListenPacket("udp", s.addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		_ = pc.Close()
		return err
	}
	s.udp.PacketConn = pc
	s.tcp.Listener = ln

	errCh := make(chan error, 2)
	go func() { errCh <- s.udp.ActivateAndServe() }()
	go func() { errCh <- s.tcp.ActivateAndServe() }()
	err = <-errCh
	s.Shutdown()
	return err
}

// Shutdown stops both listeners.
func (s *Server) Shutdown() {
	if s.udp != nil {
		_ = s.udp.Shutdown()
	}
	if s.tcp != nil {
		_ = s.tcp.Shutdown()
	}
}
