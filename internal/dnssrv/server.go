// Package dnssrv runs a local DNS server that forwards every query upstream
// over DoH. Point the OS resolver at it (e.g. 127.0.0.1) to neutralise ISP
// DNS tampering system-wide.
package dnssrv

import (
	"context"
	"log"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
)

// Server listens on UDP and TCP and relays queries to a DoH upstream.
type Server struct {
	doh     *doh.Client
	timeout time.Duration
	udp     *dns.Server
	tcp     *dns.Server
}

// New builds a server listening on addr (host:port).
func New(c *doh.Client, addr string, timeout time.Duration) *Server {
	s := &Server{doh: c, timeout: timeout}
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

// ListenAndServe starts the UDP and TCP listeners and blocks until one fails.
func (s *Server) ListenAndServe() error {
	errCh := make(chan error, 2)
	go func() { errCh <- s.udp.ListenAndServe() }()
	go func() { errCh <- s.tcp.ListenAndServe() }()
	return <-errCh
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
