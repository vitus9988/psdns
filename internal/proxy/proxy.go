// Package proxy implements local HTTP CONNECT and SOCKS5 proxies that resolve
// targets over DoH and fragment the TLS ClientHello, bypassing both DNS and
// SNI-based HTTPS blocking for any app pointed at them.
package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/frag"
	"github.com/vitus9988/psdns/internal/resolver"
)

// maxTLSRecord is the maximum TLS plaintext fragment length (RFC 8446 §5.1).
const maxTLSRecord = 16384

// minDialTimeout floors the per-IP dial budget so that, with many resolved
// addresses, no single attempt is starved into an instant failure.
const minDialTimeout = 2 * time.Second

// drainTimeout bounds how long Close waits for active connection handlers to
// return after their client conns have been closed. Closing a client conn wakes
// the relay io.Copy almost immediately, so this is only a safety net against a
// wedged handler; a closing proxy never blocks longer than this.
const drainTimeout = 250 * time.Millisecond

// dialUpstream resolves host over DoH and dials the first reachable IP. The
// returned connection has TCP_NODELAY enabled so fragment writes stay separate
// segments.
func dialUpstream(ctx context.Context, res *resolver.Resolver, host, port string, timeout time.Duration) (net.Conn, error) {
	ips, err := res.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	// Give each candidate its own slice of the overall timeout. Otherwise a
	// single unreachable address — classically an AAAA record on a host with
	// broken IPv6 — could burn the entire budget on one dial before the next
	// candidate is even tried. DialContext already caps each attempt at the
	// earlier of this Dialer.Timeout and any deadline on ctx.
	per := timeout / time.Duration(len(ips))
	if per < minDialTimeout {
		per = minDialTimeout
	}
	if per > timeout {
		per = timeout // never let one attempt exceed the overall budget
	}
	var lastErr error
	for _, ip := range ips {
		d := &net.Dialer{Timeout: per}
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetNoDelay(true)
			}
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// relay copies bytes both ways. The first client→upstream payload (the TLS
// ClientHello) is fragmented per cfg; everything after is verbatim.
//
// clientRead is the source for client→upstream bytes (a buffered reader that
// may already hold pipelined ClientHello bytes); client is the raw connection
// used for the upstream→client direction and for closing.
func relay(clientRead io.Reader, client, upstream net.Conn, cfg config.Config) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = client.Close(); _ = upstream.Close() }) }

	go func() {
		defer closeBoth()
		// Read the whole first TLS record before fragmenting: a single Read may
		// return only part of the ClientHello, which would defeat the split. Bound
		// the read with a deadline so a stalled client cannot hang the goroutine.
		_ = client.SetReadDeadline(time.Now().Add(cfg.Timeout))
		data, rerr := readFirstRecord(clientRead)
		_ = client.SetReadDeadline(time.Time{})
		if len(data) > 0 {
			if werr := frag.WriteFirst(upstream, data, cfg.Frag, cfg.FragDelay); werr != nil {
				logRelayErr("clienthello", werr)
				return
			}
		}
		if rerr != nil {
			// A read-deadline timeout only means the client stayed silent while we
			// waited for a ClientHello — e.g. a server-speaks-first protocol
			// (SMTP/IMAP/SSH) tunneled over CONNECT, where the client sends nothing
			// until it sees the server's greeting. That is not fatal: drop the
			// fragmentation attempt and relay the rest verbatim so the tunnel
			// survives. Any other error (EOF, closed conn) ends the relay.
			var ne net.Error
			if !(errors.As(rerr, &ne) && ne.Timeout()) {
				return
			}
		}
		if _, err := io.Copy(upstream, clientRead); err != nil {
			logRelayErr("client->upstream", err)
		}
	}()

	if _, err := io.Copy(client, upstream); err != nil {
		logRelayErr("upstream->client", err)
	}
	closeBoth()
}

// logRelayErr logs an unexpected relay error. Ending a relay closes both
// connections, so EOF and use-of-closed-connection are the normal stop signals
// and stay silent; anything else is surfaced to aid debugging.
func logRelayErr(dir string, err error) {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return
	}
	log.Printf("proxy: relay %s: %v", dir, err)
}

// readFirstRecord reads the first TLS record (5-byte header + body) from r so
// the entire ClientHello can be fragmented atomically. For non-TLS or malformed
// input it returns whatever was read so the caller can forward it unmodified.
func readFirstRecord(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return hdr[:0], err
	}
	if hdr[0] != 0x16 { // not a TLS handshake record
		return hdr, nil
	}
	recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
	if recLen == 0 || recLen > maxTLSRecord { // not a valid TLS plaintext fragment
		return hdr, nil
	}
	body := make([]byte, recLen)
	n, err := io.ReadFull(r, body)
	return append(hdr, body[:n]...), err
}

// connTracker tracks live client connections so a closing proxy can shut them
// down explicitly. Closing the client conn wakes relay's io.Copy, whose
// closeBoth then tears down the matching upstream too, so tracking the client
// side alone is enough to end every tunnel with a clean FIN instead of letting
// it die abruptly with the process — an abrupt death breaks a browser's HTTP/2
// session multiplexed over the CONNECT tunnel. The zero value is ready to use;
// every method is safe for concurrent use.
type connTracker struct {
	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	wg      sync.WaitGroup
	closing bool
}

// add registers a client conn. It returns false if the tracker is already
// closing (the accept-vs-Close window); the caller must then close the conn
// itself and not serve it.
func (t *connTracker) add(c net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closing {
		return false
	}
	if t.conns == nil {
		t.conns = make(map[net.Conn]struct{})
	}
	t.conns[c] = struct{}{}
	t.wg.Add(1)
	return true
}

// remove deregisters a client conn when its handler returns. It only acts on a
// conn that add accepted, so wg.Done is never called more times than wg.Add.
func (t *connTracker) remove(c net.Conn) {
	t.mu.Lock()
	if _, ok := t.conns[c]; ok {
		delete(t.conns, c)
		t.wg.Done()
	}
	t.mu.Unlock()
}

// closeAll marks the tracker closing and closes every live client conn. It is
// idempotent: a later call simply finds the surviving set (possibly empty).
func (t *connTracker) closeAll() {
	t.mu.Lock()
	t.closing = true
	for c := range t.conns {
		_ = c.Close()
	}
	t.mu.Unlock()
}

// wait blocks until every tracked handler has returned or timeout elapses,
// reporting whether it drained. Closing the conns in closeAll is what makes the
// handlers return; this just bounds the wait.
func (t *connTracker) wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { t.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
