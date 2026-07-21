package proxy

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/resolver"
)

// HTTPProxy is an HTTP CONNECT proxy (HTTPS tunnelling) with SNI-bypass. It also
// forwards plaintext (non-CONNECT) requests. The listener lifecycle (Serve,
// Close) is the embedded server; only handle is HTTP-specific.
type HTTPProxy struct {
	server
	res *resolver.Resolver
	cfg config.Config
}

// NewHTTP creates an HTTP CONNECT proxy.
func NewHTTP(res *resolver.Resolver, cfg config.Config) *HTTPProxy {
	p := &HTTPProxy{res: res, cfg: cfg}
	p.onConn = p.handle
	return p
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
	host, port := splitHostPortDefault(req.Host, "443")

	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
	upstream, err := dialUpstream(ctx, p.res, host, port, p.cfg.Timeout)
	cancel()
	if err != nil {
		log.Printf("http-proxy: dial %s failed: %v", req.Host, err)
		_, _ = fmt.Fprint(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
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
	defer func() { _ = client.Close() }()

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
			_, _ = fmt.Fprint(client, "HTTP/1.1 400 Bad Request\r\n\r\n")
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
		upstream, derr := dialUpstream(ctx, p.res, host, port, p.cfg.Timeout)
		cancel()
		if derr != nil {
			log.Printf("http-proxy: dial %s failed: %v", req.Host, derr)
			_, _ = fmt.Fprint(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}

		// Rewrite the proxy-form request to origin form and drop hop-by-hop proxy
		// headers before forwarding upstream.
		req.RequestURI = ""
		req.URL.Scheme = ""
		req.URL.Host = ""
		req.Header.Del("Proxy-Connection")
		req.Header.Del("Proxy-Authorization")

		// A protocol upgrade (e.g. a plain ws:// WebSocket) stops being
		// request/response after this exchange: forward the request, then splice
		// both connections so the 101 reply and all framed bytes flow both ways.
		// Without this the handler would read one response and close the upstream,
		// killing the WebSocket.
		if isUpgradeRequest(req) {
			_ = upstream.SetWriteDeadline(time.Now().Add(p.cfg.Timeout))
			werr := req.Write(upstream)
			_ = upstream.SetWriteDeadline(time.Time{})
			if werr != nil {
				_ = upstream.Close()
				return
			}
			tunnelPlain(br, client, upstream)
			return
		}

		// Bound the response-header read so a connected but silent origin cannot
		// hang the handler indefinitely (cleared before copying the body so a
		// slow or streaming response is not truncated). The request write is only
		// deadline-bounded when there is no body to send — a small, buffered
		// header exchange. A request that carries a body (an upload) must not be
		// bounded by the fixed timeout, or req.Write, which streams the body
		// inline, would abort a legitimately large or slow upload partway through.
		if req.ContentLength == 0 {
			_ = upstream.SetWriteDeadline(time.Now().Add(p.cfg.Timeout))
		}
		werr := req.Write(upstream)
		_ = upstream.SetWriteDeadline(time.Time{})
		if werr != nil {
			_ = upstream.Close()
			return
		}

		_ = upstream.SetReadDeadline(time.Now().Add(p.cfg.Timeout))
		resp, rerr := http.ReadResponse(bufio.NewReader(upstream), req)
		_ = upstream.SetReadDeadline(time.Time{})
		if rerr != nil {
			_ = upstream.Close()
			return
		}
		werr = resp.Write(client)
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

// isUpgradeRequest reports whether req asks to switch protocols (e.g. a plain
// WebSocket handshake): it carries an Upgrade header named by a "Connection:
// Upgrade" token. Such a request is tunnelled raw rather than forwarded as a
// single request/response pair.
func isUpgradeRequest(req *http.Request) bool {
	if req.Header.Get("Upgrade") == "" {
		return false
	}
	for _, v := range req.Header.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
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
	return splitHostPortDefault(hostport, "80")
}

func splitHostPortDefault(hostport, defaultPort string) (host, port string) {
	if hostport == "" {
		return "", ""
	}
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p
	}
	if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		unbracketed := strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
		if net.ParseIP(unbracketed) != nil {
			return unbracketed, defaultPort
		}
	}
	if net.ParseIP(hostport) != nil {
		return hostport, defaultPort
	}
	return hostport, defaultPort
}
