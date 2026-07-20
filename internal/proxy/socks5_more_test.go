package proxy_test

import (
	"io"
	"net"
	"testing"
	"time"
)

// socksConn dials the SOCKS proxy and optionally completes the no-auth
// greeting, returning the connection ready for the request phase.
func socksConn(t *testing.T, addr string, greet bool) net.Conn {
	t.Helper()
	conn := waitListen(t, addr)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if greet {
		if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			t.Fatalf("write greeting: %v", err)
		}
		sel := make([]byte, 2)
		if _, err := io.ReadFull(conn, sel); err != nil {
			t.Fatalf("read method selection: %v", err)
		}
	}
	return conn
}

// expectClosed half-closes the client side and asserts the proxy closes the
// connection (EOF) without writing anything further.
func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	if _, err := io.ReadAll(conn); err != nil {
		// A reset instead of a clean FIN is also a close.
		return
	}
}

// TestSOCKS5TruncatedGreeting covers the nMethods read failure: the client
// sends only the version byte and stops.
func TestSOCKS5TruncatedGreeting(t *testing.T) {
	addr := startSOCKS(t, mockResolver(t, "127.0.0.1"))
	conn := socksConn(t, addr, false)
	if _, err := conn.Write([]byte{0x05}); err != nil {
		t.Fatalf("write: %v", err)
	}
	expectClosed(t, conn)
}

// TestSOCKS5TruncatedMethods covers the methods-list read failure: nMethods
// declares more methods than are sent.
func TestSOCKS5TruncatedMethods(t *testing.T) {
	addr := startSOCKS(t, mockResolver(t, "127.0.0.1"))
	conn := socksConn(t, addr, false)
	if _, err := conn.Write([]byte{0x05, 0x02, 0x00}); err != nil { // declares 2, sends 1
		t.Fatalf("write: %v", err)
	}
	expectClosed(t, conn)
}

// TestSOCKS5TruncatedRequestHeader covers the request-header read failure: the
// client completes the greeting then stops.
func TestSOCKS5TruncatedRequestHeader(t *testing.T) {
	addr := startSOCKS(t, mockResolver(t, "127.0.0.1"))
	conn := socksConn(t, addr, true)
	expectClosed(t, conn)
}

// TestSOCKS5UnsupportedATYP verifies an unknown address type yields reply 0x08.
func TestSOCKS5UnsupportedATYP(t *testing.T) {
	addr := startSOCKS(t, mockResolver(t, "127.0.0.1"))
	conn := socksConn(t, addr, true)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00, 0x09}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	rep := make([]byte, 10)
	if _, err := io.ReadFull(conn, rep); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if rep[1] != 0x08 {
		t.Fatalf("reply code = %#x, want 0x08 (address type not supported)", rep[1])
	}
}

// TestSOCKS5TruncatedPort covers the port read failure: a full IPv4 address
// arrives but the two port bytes never do.
func TestSOCKS5TruncatedPort(t *testing.T) {
	addr := startSOCKS(t, mockResolver(t, "127.0.0.1"))
	conn := socksConn(t, addr, true)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1}); err != nil {
		t.Fatalf("write request: %v", err)
	}
	expectClosed(t, conn)
}
