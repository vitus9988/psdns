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
	track  connTracker
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
		if !p.track.add(conn) { // Close is already running: don't serve a doomed conn
			_ = conn.Close()
			continue
		}
		go func() {
			defer p.track.remove(conn)
			p.handle(conn)
		}()
	}
}

// Close stops the listener and shuts down every live connection so in-flight
// CONNECT tunnels end with a clean FIN instead of dying abruptly with the
// process (which breaks a browser's HTTP/2 session carried over the tunnel).
// It is safe to call concurrently with (or before) ListenAndServe, and is
// idempotent.
func (p *HTTPProxy) Close() error {
	p.mu.Lock()
	p.closed = true
	var err error
	if p.ln != nil {
		err = p.ln.Close()
	}
	p.mu.Unlock()

	// Closing the client side wakes relay, whose closeBoth then closes the
	// upstream too; drain briefly so the teardown flushes before we return.
	p.track.closeAll()
	p.track.wait(drainTimeout)
	return err
}

func (p *HTTPProxy) handle(client net.Conn) {
	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		_ = client.Close()
		return
	}
	if req.Method == http.MethodConnect {
		p.handleConnect(client, br, req)
		return
	}
	// A plaintext (non-CONNECT) request: the OS http web proxy points here too,
	// so forward it rather than rejecting it. Plaintext carries no TLS, so there
	// is no ClientHello to fragment.
	p.handlePlain(client, br, req)
}

// handleConnect tunnels an HTTPS CONNECT request: dial the target over DoH,
// reply 200, then relay bytes with the ClientHello fragmented to defeat SNI DPI.
func (p *HTTPProxy) handleConnect(client net.Conn, br *bufio.Reader, req *http.Request) {
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

// handlePlain forwards a plaintext HTTP request to the resolved origin. The OS
// http web proxy is pointed at this proxy too (not just CONNECT), so a request
// like "GET http://host/path" must be proxied. Plaintext has no TLS, so the
// SNI-fragmentation relay path is intentionally not used; this is a straight
// forwarding proxy. The client connection is kept alive for further requests;
// each request dials a fresh upstream because a keep-alive client may target a
// different host per request.
func (p *HTTPProxy) handlePlain(client net.Conn, br *bufio.Reader, req *http.Request) {
	defer client.Close()

	for {
		if req == nil {
			var rerr error
			req, rerr = http.ReadRequest(br)
			if rerr != nil {
				return // client closed the connection or sent garbage
			}
		}

		host, port := hostPortFromRequest(req)
		if host == "" {
			fmt.Fprint(client, "HTTP/1.1 400 Bad Request\r\n\r\n")
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
		upstream, derr := dialUpstream(ctx, p.res, host, port, p.cfg.Timeout)
		cancel()
		if derr != nil {
			log.Printf("http-proxy: dial %s failed: %v", req.Host, derr)
			fmt.Fprint(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}

		// Rewrite the proxy-form request to origin form and drop hop-by-hop proxy
		// headers before forwarding upstream.
		req.RequestURI = ""
		req.URL.Scheme = ""
		req.URL.Host = ""
		req.Header.Del("Proxy-Connection")
		req.Header.Del("Proxy-Authorization")

		if werr := req.Write(upstream); werr != nil {
			_ = upstream.Close()
			return
		}

		resp, rerr := http.ReadResponse(bufio.NewReader(upstream), req)
		if rerr != nil {
			_ = upstream.Close()
			return
		}
		werr := resp.Write(client)
		_ = resp.Body.Close()
		_ = upstream.Close()
		if werr != nil {
			return
		}

		// Continue only while both ends keep the connection alive.
		if req.Close || resp.Close {
			return
		}
		req = nil
	}
}

// hostPortFromRequest extracts the target host and port from a plaintext proxy
// request, defaulting to port 80. Proxy requests normally carry an absolute URL
// (req.URL.Host); an origin-form request falls back to the Host header.
func hostPortFromRequest(req *http.Request) (host, port string) {
	hostport := req.URL.Host
	if hostport == "" {
		hostport = req.Host
	}
	if hostport == "" {
		return "", ""
	}
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p
	}
	return hostport, "80"
}
