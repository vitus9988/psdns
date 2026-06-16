// Package supervisor owns the runtime config and the running server set for the
// psdns GUI. It is the single control surface the UI drives: Start/Stop/Status/
// SetConfig. It reuses the same internal packages the CLI uses (doh, resolver,
// dnssrv, proxy) and knows nothing about Wails or HTTP, so it stays trivially
// unit-testable and could back the CLI too.
package supervisor

import (
	"sync"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/dnssrv"
	"github.com/vitus9988/psdns/internal/doh"
	"github.com/vitus9988/psdns/internal/proxy"
	"github.com/vitus9988/psdns/internal/resolver"
)

// Listener is the live status of one underlying server.
type Listener struct {
	Kind string `json:"kind"` // KindDNS | KindHTTP | KindSOCKS
	Addr string `json:"addr"`
	Up   bool   `json:"up"`
	Err  string `json:"err,omitempty"` // friendly bind/serve error when Up is false
}

// State is a point-in-time snapshot returned by Status.
type State struct {
	Running   bool          `json:"running"`
	Mode      Mode          `json:"mode,omitempty"`
	Config    config.Config `json:"config"`
	Listeners []Listener    `json:"listeners"`
	StartedAt time.Time     `json:"startedAt,omitempty"`
}

// Supervisor owns the current config and the running server set. Every method is
// safe for concurrent use.
type Supervisor struct {
	mu       sync.Mutex
	cfg      config.Config
	mode     Mode
	running  bool
	stopping bool // set by Stop so serve goroutines treat their return as clean
	started  time.Time

	// Underlying servers are one-shot (their listeners cannot be reopened after
	// Close/Shutdown), so a fresh set is allocated on every Start.
	dns  *dnssrv.Server
	http *proxy.HTTPProxy
	sock *proxy.SOCKSProxy

	listeners map[string]*Listener // keyed by Kind; updated by serve goroutines
}

// New returns a Supervisor seeded with cfg (use config.Default()).
func New(cfg config.Config) *Supervisor {
	return &Supervisor{cfg: cfg, listeners: map[string]*Listener{}}
}

// Start brings up the servers for mode. It returns an error only for caller
// mistakes (already running, bad mode, bad DoH config). Per-listener bind
// failures (e.g. :53 permission denied, port in use) are NOT returned here —
// the underlying servers report them through ListenAndServe, so they surface
// asynchronously via Status().Listeners[i].Err. Use WaitSettled to give them a
// moment to appear.
func (s *Supervisor) Start(mode Mode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrAlreadyRunning
	}
	if !mode.Valid() {
		return ErrInvalidMode
	}

	client, err := doh.New(s.cfg.DoHURL, s.cfg.DoHBootstrap, s.cfg.Timeout)
	if err != nil {
		return err // bad endpoint URL — a config error worth showing inline
	}

	s.listeners = map[string]*Listener{}
	s.stopping = false
	res := resolver.New(client)

	// start records a fresh listener and runs its blocking serve loop in a
	// goroutine, capturing the bind/serve error when it returns.
	start := func(kind, addr string, serve func() error) {
		l := &Listener{Kind: kind, Addr: addr, Up: true}
		s.listeners[kind] = l
		go func() {
			serr := serve() // blocks until Close/Shutdown or a bind failure
			s.mu.Lock()
			l.Up = false
			if serr != nil && !s.stopping && !isClosed(serr) {
				l.Err = classifyBindErr(kind, addr, serr)
			}
			s.mu.Unlock()
		}()
	}

	switch mode {
	case ModeProxy:
		s.http = proxy.NewHTTP(res, s.cfg)
		s.sock = proxy.NewSOCKS(res, s.cfg)
		start(KindHTTP, s.cfg.ProxyListen, s.http.ListenAndServe)
		start(KindSOCKS, s.cfg.SocksListen, s.sock.ListenAndServe)
	case ModeResolve:
		s.dns = dnssrv.New(client, s.cfg.DNSListen, s.cfg.Timeout)
		start(KindDNS, s.cfg.DNSListen, s.dns.ListenAndServe)
	case ModeRun:
		s.dns = dnssrv.New(client, s.cfg.DNSListen, s.cfg.Timeout)
		s.http = proxy.NewHTTP(res, s.cfg)
		s.sock = proxy.NewSOCKS(res, s.cfg)
		start(KindDNS, s.cfg.DNSListen, s.dns.ListenAndServe)
		start(KindHTTP, s.cfg.ProxyListen, s.http.ListenAndServe)
		start(KindSOCKS, s.cfg.SocksListen, s.sock.ListenAndServe)
	}

	s.mode = mode
	s.running = true
	s.started = time.Now()
	return nil
}

// Stop tears down all running servers. The instances are discarded; the next
// Start allocates fresh ones.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return ErrNotRunning
	}
	s.stopping = true
	if s.http != nil {
		_ = s.http.Close()
	}
	if s.sock != nil {
		_ = s.sock.Close()
	}
	if s.dns != nil {
		s.dns.Shutdown()
	}
	s.http, s.sock, s.dns = nil, nil, nil
	s.running = false
	s.mode = ""
	return nil
}

// Status returns a snapshot of the current state. Listeners are reported in a
// stable order (dns, http, socks) for predictable UI rendering and tests.
func (s *Supervisor) Status() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Running: s.running, Mode: s.mode, Config: s.cfg}
	if s.running {
		st.StartedAt = s.started
	}
	for _, kind := range []string{KindDNS, KindHTTP, KindSOCKS} {
		if l, ok := s.listeners[kind]; ok {
			st.Listeners = append(st.Listeners, *l)
		}
	}
	return st
}

// Config returns the current configuration.
func (s *Supervisor) Config() config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// SetConfig replaces the config. It is rejected while running, so the live
// servers always match the reported config; changes apply on the next Start.
func (s *Supervisor) SetConfig(c config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrAlreadyRunning
	}
	s.cfg = c
	return nil
}

// WaitSettled sleeps a short interval then returns the current Status. Start is
// non-blocking and bind failures land a moment later, so callers that want the
// failure reflected in the response (e.g. the GUI's Start handler) use this.
func (s *Supervisor) WaitSettled(d time.Duration) State {
	time.Sleep(d)
	return s.Status()
}
