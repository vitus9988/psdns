package frag

import (
	"bytes"
	"testing"

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
