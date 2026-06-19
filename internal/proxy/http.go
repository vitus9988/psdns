package proxy

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/resolver"
)

// HTTPProxy is an HTTP CONNECT proxy (HTTPS tunnelling) with SNI-bypass.
type HTTPProxy struct {
	res *resolver.Resolver
	cfg config.Config

	mu     sync.Mutex
	ln     net.Listener
	closed bool
}

// NewHTTP creates an HTTP CONNECT proxy.
func NewHTTP(res *resolver.Resolver, cfg config.Config) *HTTPProxy {
	return &HTTPProxy{res: res, cfg: cfg}
}

// ListenAndServe binds cfg.ProxyListen and serves until the listener is closed.
// It is the CLI entry point and keeps strict single-port behavior (no fallback).
// It is safe to call Close concurrently, including before the listener is bound.
func (p *HTTPProxy) ListenAndServe() error {
	ln, err := net.Listen("tcp", p.cfg.ProxyListen)
	if err != nil {
		return err
	}
	return p.Serve(ln)
}

// Serve accepts connections on ln until it is closed via Close. Serve adopts ln
// (Close shuts it down) and is safe to call Close before or concurrently with
// Serve. The GUI supervisor uses Serve directly so it can bind with port
// fallback and report the actual bound address.
func (p *HTTPProxy) Serve(ln net.Listener) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = ln.Close()
		return net.ErrClosed
	}
	p.ln = ln
	p.mu.Unlock()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go p.handle(conn)
	}
}

// Close stops the listener. It is safe to call concurrently with (or before)
// ListenAndServe.
func (p *HTTPProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.ln != nil {
		return p.ln.Close()
	}
	return nil
}

func (p *HTTPProxy) handle(client net.Conn) {
	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		_ = client.Close()
		return
	}
	if req.Method != http.MethodConnect {
		// Only HTTPS tunnelling (the SNI-block target) is supported.
		fmt.Fprint(client, "HTTP/1.1 501 Not Implemented\r\n\r\n")
		_ = client.Close()
		return
	}

	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		host, port = req.Host, "443"
	}

	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
	upstream, err := dialUpstream(ctx, p.res, host, port, p.cfg.Timeout)
	cancel()
	if err != nil {
		log.Printf("http-proxy: dial %s failed: %v", req.Host, err)
		fmt.Fprint(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		_ = client.Close()
		return
	}

	if _, err := fmt.Fprint(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}

	// br may already hold ClientHello bytes the client pipelined after CONNECT,
	// so the client→upstream direction must read from br, not the raw conn.
	relay(br, client, upstream, p.cfg)
}
