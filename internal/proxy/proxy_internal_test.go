package proxy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// tlsRecord builds a TLS handshake record (content type 0x16) whose 2-byte
// length field equals declaredLen, carrying bodyLen body bytes. When
// declaredLen != bodyLen the header lies, which exercises the partial-read path.
func tlsRecord(declaredLen, bodyLen int) []byte {
	hdr := []byte{0x16, 0x03, 0x01, byte(declaredLen >> 8), byte(declaredLen)}
	body := bytes.Repeat([]byte{0xAB}, bodyLen)
	return append(hdr, body...)
}

func TestReadFirstRecordFullTLS(t *testing.T) {
	const bodyLen = 40
	rec := tlsRecord(bodyLen, bodyLen)

	got, err := readFirstRecord(bufio.NewReader(bytes.NewReader(rec)))
	if err != nil {
		t.Fatalf("readFirstRecord: %v", err)
	}
	if !bytes.Equal(got, rec) {
		t.Fatalf("returned %d bytes, want the full %d-byte record", len(got), len(rec))
	}
}

func TestReadFirstRecordTrailingBytesNotConsumed(t *testing.T) {
	const bodyLen = 16
	rec := tlsRecord(bodyLen, bodyLen)
	trailing := []byte("APPLICATION-DATA-AFTER-HELLO")
	r := bufio.NewReader(bytes.NewReader(append(append([]byte(nil), rec...), trailing...)))

	got, err := readFirstRecord(r)
	if err != nil {
		t.Fatalf("readFirstRecord: %v", err)
	}
	if !bytes.Equal(got, rec) {
		t.Fatalf("first record mismatch: got %d bytes", len(got))
	}
	rest, _ := io.ReadAll(r)
	if !bytes.Equal(rest, trailing) {
		t.Fatalf("trailing bytes corrupted: got %q want %q", rest, trailing)
	}
}

func TestReadFirstRecordNonTLSPassthrough(t *testing.T) {
	// First byte != 0x16 -> returns the 5 header bytes unmodified, nil error.
	data := []byte("GET / HTTP/1.1\r\n")
	got, err := readFirstRecord(bufio.NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatalf("expected nil error for non-TLS, got %v", err)
	}
	if !bytes.Equal(got, data[:5]) {
		t.Fatalf("non-TLS passthrough returned %q, want first 5 bytes %q", got, data[:5])
	}
}

func TestReadFirstRecordZeroLenGuard(t *testing.T) {
	// recLen == 0 is not a valid plaintext fragment -> header returned as-is.
	hdr := []byte{0x16, 0x03, 0x01, 0x00, 0x00}
	got, err := readFirstRecord(bufio.NewReader(bytes.NewReader(hdr)))
	if err != nil {
		t.Fatalf("expected nil error for zero recLen, got %v", err)
	}
	if !bytes.Equal(got, hdr) {
		t.Fatalf("zero recLen returned %v, want header %v", got, hdr)
	}
}

func TestReadFirstRecordOversizedGuard(t *testing.T) {
	// recLen > maxTLSRecord (16384) must be rejected without attempting to read
	// the bogus body; only the 5 header bytes come back.
	oversize := maxTLSRecord + 1
	hdr := []byte{0x16, 0x03, 0x01, byte(oversize >> 8), byte(oversize)}
	// Provide far fewer body bytes than declared; the guard must trip first.
	in := append(append([]byte(nil), hdr...), bytes.Repeat([]byte{0x00}, 10)...)

	got, err := readFirstRecord(bufio.NewReader(bytes.NewReader(in)))
	if err != nil {
		t.Fatalf("expected nil error for oversized recLen, got %v", err)
	}
	if !bytes.Equal(got, hdr) {
		t.Fatalf("oversized guard returned %d bytes, want 5-byte header", len(got))
	}
	if binary.BigEndian.Uint16(got[3:5]) != uint16(oversize) {
		t.Fatalf("header length field altered")
	}
}

func TestReadFirstRecordHeaderEOF(t *testing.T) {
	// Fewer than 5 header bytes -> ReadFull returns an error and an empty slice.
	got, err := readFirstRecord(bufio.NewReader(bytes.NewReader([]byte{0x16, 0x03})))
	if err == nil {
		t.Fatalf("expected error on short header")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected error type: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("short header must return empty slice, got %d bytes", len(got))
	}
}

func TestReadFirstRecordBodyEOF(t *testing.T) {
	// Header declares 100 body bytes but only 30 are available: readFirstRecord
	// returns the header + the partial body it managed to read, plus an error,
	// so the caller can still forward what arrived.
	const declared = 100
	const have = 30
	rec := tlsRecord(declared, have)

	got, err := readFirstRecord(bufio.NewReader(bytes.NewReader(rec)))
	if err == nil {
		t.Fatalf("expected error on truncated body")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected error type: %v", err)
	}
	wantLen := 5 + have
	if len(got) != wantLen {
		t.Fatalf("partial body: got %d bytes, want header+partial = %d", len(got), wantLen)
	}
	if !bytes.Equal(got, rec) {
		t.Fatalf("partial bytes differ from what was sent")
	}
}

func TestLogRelayErr(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// The normal end of a relay (EOF / closed conn) must stay silent.
	for _, err := range []error{nil, io.EOF, net.ErrClosed} {
		buf.Reset()
		logRelayErr("dir", err)
		if buf.Len() != 0 {
			t.Errorf("expected silence for %v, logged %q", err, buf.String())
		}
	}

	// An unexpected error is surfaced.
	buf.Reset()
	logRelayErr("upstream->client", errors.New("boom"))
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("unexpected error not logged: %q", buf.String())
	}
}

func TestReadAddrIPv4(t *testing.T) {
	br := bufio.NewReader(bytes.NewReader([]byte{1, 2, 3, 4}))
	host, ok := readAddr(br, 0x01)
	if !ok || host != "1.2.3.4" {
		t.Fatalf("IPv4 readAddr = %q,%v, want 1.2.3.4,true", host, ok)
	}
}

func TestReadAddrDomain(t *testing.T) {
	name := "example.com"
	buf := append([]byte{byte(len(name))}, []byte(name)...)
	br := bufio.NewReader(bytes.NewReader(buf))
	host, ok := readAddr(br, 0x03)
	if !ok || host != name {
		t.Fatalf("domain readAddr = %q,%v, want %q,true", host, ok, name)
	}
}

func TestReadAddrIPv6(t *testing.T) {
	raw := []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01}
	br := bufio.NewReader(bytes.NewReader(raw))
	host, ok := readAddr(br, 0x04)
	if !ok || host != "2001:db8::1" {
		t.Fatalf("IPv6 readAddr = %q,%v, want 2001:db8::1,true", host, ok)
	}
}

func TestHostPortFromRequest(t *testing.T) {
	cases := []struct {
		name               string
		rawURL             string
		hostHdr            string
		wantHost, wantPort string
	}{
		{"absolute with port", "http://example.com:8080/p", "", "example.com", "8080"},
		{"absolute no port defaults 80", "http://example.com/p", "", "example.com", "80"},
		{"absolute bracketed IPv6 defaults 80", "http://[::1]/p", "", "::1", "80"},
		{"absolute bracketed IPv6 with port", "http://[::1]:8080/p", "", "::1", "8080"},
		{"origin form uses host header", "/p", "example.com", "example.com", "80"},
		{"host header with port", "/p", "example.com:8443", "example.com", "8443"},
		{"host header bracketed IPv6", "/p", "[::1]", "::1", "80"},
		{"host header bracketed IPv6 with port", "/p", "[::1]:8080", "::1", "8080"},
		{"no host anywhere", "/p", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.rawURL)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.rawURL, err)
			}
			req := &http.Request{URL: u, Host: tc.hostHdr}
			host, port := hostPortFromRequest(req)
			if host != tc.wantHost || port != tc.wantPort {
				t.Fatalf("hostPortFromRequest = %q,%q want %q,%q", host, port, tc.wantHost, tc.wantPort)
			}
		})
	}
}

func TestSplitHostPortDefault(t *testing.T) {
	cases := []struct {
		hostport           string
		defaultPort        string
		wantHost, wantPort string
	}{
		{"example.com:443", "80", "example.com", "443"},
		{"example.com", "80", "example.com", "80"},
		{"[::1]:443", "80", "::1", "443"},
		{"[::1]", "80", "::1", "80"},
		{"::1", "80", "::1", "80"},
		{"", "80", "", ""},
	}
	for _, tc := range cases {
		host, port := splitHostPortDefault(tc.hostport, tc.defaultPort)
		if host != tc.wantHost || port != tc.wantPort {
			t.Errorf("splitHostPortDefault(%q,%q) = %q,%q want %q,%q",
				tc.hostport, tc.defaultPort, host, port, tc.wantHost, tc.wantPort)
		}
	}
}

func TestReadAddrUnsupportedType(t *testing.T) {
	br := bufio.NewReader(bytes.NewReader([]byte{0, 0}))
	if host, ok := readAddr(br, 0x09); ok {
		t.Fatalf("unsupported ATYP should fail, got %q,true", host)
	}
}

func TestReadAddrTruncated(t *testing.T) {
	// IPv4 ATYP but only 2 of 4 bytes available.
	br := bufio.NewReader(bytes.NewReader([]byte{1, 2}))
	if _, ok := readAddr(br, 0x01); ok {
		t.Fatalf("truncated IPv4 addr should fail")
	}
}

// TestConnTrackerAddAfterCloseRejected covers the accept-vs-Close race window:
// once closeAll has run, add must refuse new conns (so Serve closes them) and
// closeAll must have closed the conn it was already tracking.
func TestConnTrackerAddAfterCloseRejected(t *testing.T) {
	var tr connTracker
	c1, peer1 := net.Pipe()
	defer c1.Close()
	defer peer1.Close()
	if !tr.add(c1) {
		t.Fatal("add on a fresh tracker should succeed")
	}
	tr.closeAll() // marks the tracker closing and closes c1

	// closeAll closed the tracked conn: the peer's read wakes with an error.
	errCh := make(chan error, 1)
	go func() { _, e := peer1.Read(make([]byte, 1)); errCh <- e }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("closeAll should have closed the tracked conn")
		}
	case <-time.After(time.Second):
		t.Fatal("closeAll did not close the tracked conn (peer read still blocked)")
	}

	c2, peer2 := net.Pipe()
	defer c2.Close()
	defer peer2.Close()
	if tr.add(c2) {
		t.Fatal("add after closeAll must be rejected")
	}

	tr.remove(c1) // the c1 handler returns; the WaitGroup drains
	if !tr.wait(time.Second) {
		t.Fatal("wait did not drain after the only handler returned")
	}
}

// TestConnTrackerCloseAllIdempotent verifies a second Close (Stop then Shutdown
// both reach Close) neither panics nor blocks.
func TestConnTrackerCloseAllIdempotent(t *testing.T) {
	var tr connTracker
	tr.closeAll()
	tr.closeAll() // must not panic
	if !tr.wait(time.Second) {
		t.Fatal("empty tracker wait should return true immediately")
	}
}

// TestConnTrackerWaitTimesOutWhileHandlerOutstanding verifies wait is bounded:
// while a handler is still registered it times out, then drains after remove.
func TestConnTrackerWaitTimesOutWhileHandlerOutstanding(t *testing.T) {
	var tr connTracker
	c, peer := net.Pipe()
	defer c.Close()
	defer peer.Close()
	if !tr.add(c) {
		t.Fatal("add should succeed")
	}
	if tr.wait(50 * time.Millisecond) {
		t.Fatal("wait should time out while a handler is outstanding")
	}
	tr.remove(c)
	if !tr.wait(time.Second) {
		t.Fatal("wait should drain after remove")
	}
}
