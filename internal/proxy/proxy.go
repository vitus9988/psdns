// Package proxy implements local HTTP CONNECT and SOCKS5 proxies that resolve
// targets over DoH and fragment the TLS ClientHello, bypassing both DNS and
// SNI-based HTTPS blocking for any app pointed at them.
package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/frag"
	"github.com/vitus9988/psdns/internal/resolver"
)

// maxTLSRecord is the maximum TLS plaintext fragment length (RFC 8446 §5.1).
const maxTLSRecord = 16384

// dialUpstream resolves host over DoH and dials the first reachable IP. The
// returned connection has TCP_NODELAY enabled so fragment writes stay separate
// segments.
func dialUpstream(ctx context.Context, res *resolver.Resolver, host, port string, timeout time.Duration) (net.Conn, error) {
	ips, err := res.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: timeout}
	var lastErr error
	for _, ip := range ips {
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
				return
			}
		}
		if rerr != nil {
			return
		}
		_, _ = io.Copy(upstream, clientRead)
	}()

	_, _ = io.Copy(client, upstream)
	closeBoth()
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
