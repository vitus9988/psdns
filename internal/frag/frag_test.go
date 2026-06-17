package frag

import (
	"bytes"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
)

// recWriter records each Write as a separate segment.
type recWriter struct{ writes [][]byte }

func (c *recWriter) Write(p []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}

// buildClientHello constructs a minimal but well-formed TLS ClientHello record
// carrying the given SNI host name.
func buildClientHello(sni string) []byte {
	name := []byte(sni)

	entry := []byte{0x00, byte(len(name) >> 8), byte(len(name))} // host_name entry
	entry = append(entry, name...)
	snList := append([]byte{byte(len(entry) >> 8), byte(len(entry))}, entry...)
	ext := append([]byte{0x00, 0x00, byte(len(snList) >> 8), byte(len(snList))}, snList...)

	body := []byte{0x03, 0x03}                  // client_version TLS 1.2
	body = append(body, make([]byte, 32)...)    // random
	body = append(body, 0x00)                   // session_id length 0
	body = append(body, 0x00, 0x02, 0x13, 0x01) // cipher_suites: len 2 + TLS_AES_128_GCM_SHA256
	body = append(body, 0x01, 0x00)             // compression_methods: len 1 + null
	body = append(body, byte(len(ext)>>8), byte(len(ext)))
	body = append(body, ext...)

	hs := []byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	hs = append(hs, body...)

	rec := []byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}
	return append(rec, hs...)
}

func TestSNISplitOffset(t *testing.T) {
	const sni = "blocked.example.com"
	rec := buildClientHello(sni)

	off, ok := sniSplitOffset(rec)
	if !ok {
		t.Fatalf("expected to locate SNI, got ok=false")
	}
	idx := bytes.Index(rec, []byte(sni))
	if idx < 0 {
		t.Fatalf("test bug: SNI not present in record")
	}
	if off <= idx || off >= idx+len(sni) {
		t.Fatalf("split offset %d not strictly inside SNI [%d,%d)", off, idx, idx+len(sni))
	}
}

func TestSNISplitOffsetNonClientHello(t *testing.T) {
	if _, ok := sniSplitOffset([]byte("not a tls record at all")); ok {
		t.Fatalf("expected ok=false for non-ClientHello input")
	}
}

func TestWriteSplitSeparatesSNI(t *testing.T) {
	const sni = "blocked.example.com"
	rec := buildClientHello(sni)

	w := &recWriter{}
	if err := WriteFirst(w, rec, config.FragSplit, 0); err != nil {
		t.Fatalf("WriteFirst: %v", err)
	}
	if len(w.writes) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(w.writes))
	}
	got := append(append([]byte(nil), w.writes[0]...), w.writes[1]...)
	if !bytes.Equal(got, rec) {
		t.Fatalf("reassembled bytes differ from original")
	}
	if bytes.Contains(w.writes[0], []byte(sni)) || bytes.Contains(w.writes[1], []byte(sni)) {
		t.Fatalf("SNI must not be wholly contained in either segment")
	}
}

func TestWriteTLSRecordsSplitsSNI(t *testing.T) {
	const sni = "blocked.example.com"
	rec := buildClientHello(sni)
	payload := rec[5:]

	w := &recWriter{}
	if err := WriteFirst(w, rec, config.FragTLSRecord, 0); err != nil {
		t.Fatalf("WriteFirst: %v", err)
	}
	if len(w.writes) != 2 {
		t.Fatalf("expected 2 records, got %d", len(w.writes))
	}
	for i, r := range w.writes {
		if len(r) < 5 || r[0] != 0x16 {
			t.Fatalf("record %d is not a TLS handshake record", i)
		}
	}
	// Concatenated record payloads must equal the original handshake payload.
	merged := append(append([]byte(nil), w.writes[0][5:]...), w.writes[1][5:]...)
	if !bytes.Equal(merged, payload) {
		t.Fatalf("record payloads do not reassemble the handshake")
	}
	if bytes.Contains(w.writes[0][5:], []byte(sni)) || bytes.Contains(w.writes[1][5:], []byte(sni)) {
		t.Fatalf("SNI must be split across records")
	}
}

func TestWriteFirstNone(t *testing.T) {
	rec := buildClientHello("example.com")
	w := &recWriter{}
	if err := WriteFirst(w, rec, config.FragNone, 0); err != nil {
		t.Fatalf("WriteFirst: %v", err)
	}
	if len(w.writes) != 1 || !bytes.Equal(w.writes[0], rec) {
		t.Fatalf("none strategy must write the payload unchanged in one write")
	}
}

func TestWriteTLSRecordsNonTLSUnchanged(t *testing.T) {
	data := []byte("plain non-tls payload bytes")
	w := &recWriter{}
	if err := WriteFirst(w, data, config.FragTLSRecord, 0); err != nil {
		t.Fatalf("WriteFirst: %v", err)
	}
	if len(w.writes) != 1 || !bytes.Equal(w.writes[0], data) {
		t.Fatalf("non-TLS payload must be written unmodified")
	}
}

func TestFallbackOffset(t *testing.T) {
	cases := []struct{ n, want int }{
		{0, 0}, {1, 0}, {2, 1}, {6, 1}, {7, 6}, {100, 6},
	}
	for _, c := range cases {
		if got := fallbackOffset(c.n); got != c.want {
			t.Errorf("fallbackOffset(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

// TestWriteSplitFallbackWhenNoSNI feeds a handshake-looking record with no
// parseable SNI: writeSplit must fall back to a fixed offset and still emit two
// segments that reassemble to the original.
func TestWriteSplitFallbackWhenNoSNI(t *testing.T) {
	data := append([]byte{0x16, 0x03, 0x01, 0x00, 0x10}, make([]byte, 16)...)
	w := &recWriter{}
	if err := WriteFirst(w, data, config.FragSplit, 0); err != nil {
		t.Fatalf("WriteFirst: %v", err)
	}
	if len(w.writes) != 2 {
		t.Fatalf("expected fallback split into 2 segments, got %d", len(w.writes))
	}
	merged := append(append([]byte(nil), w.writes[0]...), w.writes[1]...)
	if !bytes.Equal(merged, data) {
		t.Fatalf("reassembled bytes differ from original")
	}
}

// TestWriteSplitShortPayloadWritesWhole covers the guard where no usable split
// offset exists, so the payload is written unchanged.
func TestWriteSplitShortPayloadWritesWhole(t *testing.T) {
	data := []byte{0x16}
	w := &recWriter{}
	if err := WriteFirst(w, data, config.FragSplit, 0); err != nil {
		t.Fatalf("WriteFirst: %v", err)
	}
	if len(w.writes) != 1 || !bytes.Equal(w.writes[0], data) {
		t.Fatalf("short payload must be written whole in one segment")
	}
}

// TestWriteTLSRecordsTinyPayloadWritesWhole covers the sp<=0 guard: a 1-byte
// payload cannot be re-framed and is written unchanged.
func TestWriteTLSRecordsTinyPayloadWritesWhole(t *testing.T) {
	data := []byte{0x16, 0x03, 0x01, 0x00, 0x01, 0xAB}
	w := &recWriter{}
	if err := WriteFirst(w, data, config.FragTLSRecord, 0); err != nil {
		t.Fatalf("WriteFirst: %v", err)
	}
	if len(w.writes) != 1 || !bytes.Equal(w.writes[0], data) {
		t.Fatalf("tiny payload must be written unchanged")
	}
}

// TestSNISplitOffsetTruncatedDoesNotPanic feeds progressive truncations of a
// valid ClientHello; each must fail gracefully (no panic, no out-of-range off),
// exercising the bounds checks in the parser and the length-vector skips.
func TestSNISplitOffsetTruncatedDoesNotPanic(t *testing.T) {
	full := buildClientHello("blocked.example.com")
	for n := 0; n <= len(full); n++ {
		if off, ok := sniSplitOffset(full[:n]); ok && (off <= 0 || off >= n) {
			t.Fatalf("truncation %d returned ok with out-of-range off %d", n, off)
		}
	}
}

// TestWriteWithDelay exercises the inter-fragment delay branch of both strategies.
func TestWriteWithDelay(t *testing.T) {
	rec := buildClientHello("blocked.example.com")
	for _, strat := range []config.FragStrategy{config.FragSplit, config.FragTLSRecord} {
		w := &recWriter{}
		if err := WriteFirst(w, rec, strat, time.Millisecond); err != nil {
			t.Fatalf("WriteFirst(%s): %v", strat, err)
		}
		if len(w.writes) != 2 {
			t.Fatalf("WriteFirst(%s): expected 2 writes, got %d", strat, len(w.writes))
		}
	}
}
