package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"log"
	"net"
	"strconv"
	"sync"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/resolver"
)

// SOCKSProxy is a minimal SOCKS5 proxy (no-auth, CONNECT only) with SNI-bypass.
type SOCKSProxy struct {
	res *resolver.Resolver
	cfg config.Config

	mu     sync.Mutex
	ln     net.Listener
	closed bool
}

// NewSOCKS creates a SOCKS5 proxy.
func NewSOCKS(res *resolver.Resolver, cfg config.Config) *SOCKSProxy {
	return &SOCKSProxy{res: res, cfg: cfg}
}

// ListenAndServe accepts connections until the listener is closed. It is safe
// to call Close concurrently, including before the listener is bound.
func (p *SOCKSProxy) ListenAndServe() error {
	ln, err := net.Listen("tcp", p.cfg.SocksListen)
	if err != nil {
		return err
	}
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
func (p *SOCKSProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.ln != nil {
		return p.ln.Close()
	}
	return nil
}

func (p *SOCKSProxy) handle(client net.Conn) {
	br := bufio.NewReader(client)

	// Greeting: VER, NMETHODS, METHODS...
	ver, err := br.ReadByte()
	if err != nil || ver != 0x05 {
		_ = client.Close()
		return
	}
	nMethods, err := br.ReadByte()
	if err != nil {
		_ = client.Close()
		return
	}
	if _, err := io.CopyN(io.Discard, br, int64(nMethods)); err != nil {
		_ = client.Close()
		return
	}
	// Select "no authentication required".
	if _, err := client.Write([]byte{0x05, 0x00}); err != nil {
		_ = client.Close()
		return
	}

	// Request: VER, CMD, RSV, ATYP, ADDR, PORT
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		_ = client.Close()
		return
	}
	if hdr[0] != 0x05 || hdr[1] != 0x01 { // CONNECT only
		reply(client, 0x07) // command not supported
		_ = client.Close()
		return
	}

	host, ok := readAddr(br, hdr[3])
	if !ok {
		reply(client, 0x08) // address type not supported
		_ = client.Close()
		return
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(br, portBuf); err != nil {
		_ = client.Close()
		return
	}
	port := strconv.Itoa(int(binary.BigEndian.Uint16(portBuf)))

	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.Timeout)
	upstream, err := dialUpstream(ctx, p.res, host, port, p.cfg.Timeout)
	cancel()
	if err != nil {
		log.Printf("socks: dial %s:%s failed: %v", host, port, err)
		reply(client, 0x05) // connection refused
		_ = client.Close()
		return
	}

	reply(client, 0x00) // succeeded
	relay(br, client, upstream, p.cfg)
}

// readAddr reads a SOCKS5 address of the given ATYP and returns it as a host
// string (IP literal or domain name).
func readAddr(br *bufio.Reader, atyp byte) (string, bool) {
	switch atyp {
	case 0x01: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", false
		}
		return net.IP(b).String(), true
	case 0x03: // domain name
		l, err := br.ReadByte()
		if err != nil {
			return "", false
		}
		b := make([]byte, int(l))
		if _, err := io.ReadFull(br, b); err != nil {
			return "", false
		}
		return string(b), true
	case 0x04: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return "", false
		}
		return net.IP(b).String(), true
	default:
		return "", false
	}
}

// reply writes a SOCKS5 reply with the given status and a zero BND address.
func reply(w io.Writer, status byte) {
	_, _ = w.Write([]byte{0x05, status, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}
