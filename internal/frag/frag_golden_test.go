package frag

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/vitus9988/psdns/internal/config"
)

// TestSplitGoldenBytes pins the exact split geometry for a fixed ClientHello so
// that any change to the parser or the split arithmetic fails loudly instead of
// silently shifting where the SNI is cut. This is the regression guard the
// fragmentation behavior leans on; update the constants below only with a
// deliberate, reviewed change.
//
// buildClientHello("blocked.example.com") lays out an 80-byte record:
//
//	record header        5 bytes   [0x16 0x03 0x01 00 4B]
//	handshake header     4 bytes   [0x01 00 00 47]
//	client_version       2
//	random              32
//	session_id           1 (len 0)
//	cipher_suites        4 (len 2)
//	compression          2 (len 1)
//	ext_total            2
//	server_name ext     28  -> SNI host "blocked.example.com" (19) at offset 61
//
// sniSplitOffset returns 61 + 19/2 = 70, so split emits [0:70] and [70:80].
func TestSplitGoldenBytes(t *testing.T) {
	const sni = "blocked.example.com"
	hello := buildClientHello(sni)
	if len(hello) != 80 {
		t.Fatalf("fixture changed: record len = %d, want 80", len(hello))
	}
	if got := bytes.Index(hello, []byte(sni)); got != 61 {
		t.Fatalf("fixture changed: SNI at %d, want 61", got)
	}

	off, ok := sniSplitOffset(hello)
	if !ok || off != 70 {
		t.Fatalf("sniSplitOffset = %d, ok=%v; want 70, true", off, ok)
	}

	// split: two segments of exactly 70 and 10 bytes.
	w := &recWriter{}
	if err := WriteFirst(w, hello, config.FragSplit, 0); err != nil {
		t.Fatalf("split WriteFirst: %v", err)
	}
	if len(w.writes) != 2 || len(w.writes[0]) != 70 || len(w.writes[1]) != 10 {
		t.Fatalf("split segment lengths = %v, want [70 10]", segLens(w.writes))
	}

	// tls-record: record0 carries 65 payload bytes (5+65=70), record1 carries 10
	// (5+10=15); the declared length fields must match.
	w = &recWriter{}
	if err := WriteFirst(w, hello, config.FragTLSRecord, 0); err != nil {
		t.Fatalf("tls-record WriteFirst: %v", err)
	}
	if len(w.writes) != 2 || len(w.writes[0]) != 70 || len(w.writes[1]) != 15 {
		t.Fatalf("tls-record lengths = %v, want [70 15]", segLens(w.writes))
	}
	if n := binary.BigEndian.Uint16(w.writes[0][3:5]); n != 65 {
		t.Fatalf("record0 declared length = %d, want 65", n)
	}
	if n := binary.BigEndian.Uint16(w.writes[1][3:5]); n != 10 {
		t.Fatalf("record1 declared length = %d, want 10", n)
	}
}

func segLens(ws [][]byte) []int {
	out := make([]int, len(ws))
	for i, w := range ws {
		out[i] = len(w)
	}
	return out
}

// ext serializes a TLS extension (type, length, body) for buildHello.
func ext(typ uint16, body []byte) []byte {
	out := []byte{byte(typ >> 8), byte(typ), byte(len(body) >> 8), byte(len(body))}
	return append(out, body...)
}

// buildHello builds a ClientHello record carrying sni, optionally preceded by
// the given extra extensions, so the SNI need not be the first extension — this
// exercises the parser's extension-walk loop on DPI-shaped inputs.
func buildHello(sni string, leading ...[]byte) []byte {
	name := []byte(sni)
	entry := append([]byte{0x00, byte(len(name) >> 8), byte(len(name))}, name...)
	snList := append([]byte{byte(len(entry) >> 8), byte(len(entry))}, entry...)
	snExt := ext(0x0000, snList)

	var exts []byte
	for _, e := range leading {
		exts = append(exts, e...)
	}
	exts = append(exts, snExt...)

	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00)                   // session_id length 0
	body = append(body, 0x00, 0x02, 0x13, 0x01) // cipher_suites
	body = append(body, 0x01, 0x00)             // compression_methods
	body = append(body, byte(len(exts)>>8), byte(len(exts)))
	body = append(body, exts...)

	hs := append([]byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
	return append([]byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}, hs...)
}

// TestWriteSplitDPIScenarios checks the split invariants — exact reassembly and
// "SNI never wholly inside one segment/record" — across DPI-shaped ClientHellos:
// short and very long host names, and an SNI placed after GREASE and padding
// extensions. Both strategies must hold the invariants for every case.
func TestWriteSplitDPIScenarios(t *testing.T) {
	grease := ext(0x0a0a, nil)               // GREASE extension, empty body
	padding := ext(0x0015, make([]byte, 24)) // padding extension before SNI
	longSNI := strings.Repeat("x", 180) + ".example.com"

	cases := []struct {
		name  string
		hello []byte
		sni   string
	}{
		{"short", buildHello("a.io"), "a.io"},
		{"typical", buildHello("blocked.example.com"), "blocked.example.com"},
		{"long", buildHello(longSNI), longSNI},
		{"after-grease-padding", buildHello("late.example.net", grease, padding), "late.example.net"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off, ok := sniSplitOffset(tc.hello)
			if !ok {
				t.Fatalf("parser failed to locate SNI")
			}
			idx := bytes.Index(tc.hello, []byte(tc.sni))
			if idx < 0 || off <= idx || off >= idx+len(tc.sni) {
				t.Fatalf("split offset %d not strictly inside SNI [%d,%d)", off, idx, idx+len(tc.sni))
			}

			for _, strat := range []config.FragStrategy{config.FragSplit, config.FragTLSRecord} {
				w := &recWriter{}
				if err := WriteFirst(w, tc.hello, strat, 0); err != nil {
					t.Fatalf("%s: WriteFirst: %v", strat, err)
				}
				if len(w.writes) != 2 {
					t.Fatalf("%s: want 2 segments, got %d", strat, len(w.writes))
				}
				assertSNISplit(t, strat, w.writes, tc.hello, tc.sni)
			}
		})
	}
}

// assertSNISplit verifies a two-segment fragmentation result: the pieces
// reassemble to the original and the SNI host name is not wholly contained in
// either piece. For tls-record the comparison is on record payloads (after the
// 5-byte header).
func assertSNISplit(t *testing.T, strat config.FragStrategy, writes [][]byte, hello []byte, sni string) {
	t.Helper()
	switch strat {
	case config.FragSplit:
		merged := append(append([]byte(nil), writes[0]...), writes[1]...)
		if !bytes.Equal(merged, hello) {
			t.Fatalf("split: reassembled bytes differ from original")
		}
		if bytes.Contains(writes[0], []byte(sni)) || bytes.Contains(writes[1], []byte(sni)) {
			t.Fatalf("split: SNI wholly contained in one segment")
		}
	case config.FragTLSRecord:
		for i, r := range writes {
			if len(r) < 5 || r[0] != 0x16 {
				t.Fatalf("tls-record: write %d is not a handshake record", i)
			}
		}
		merged := append(append([]byte(nil), writes[0][5:]...), writes[1][5:]...)
		if !bytes.Equal(merged, hello[5:]) {
			t.Fatalf("tls-record: record payloads do not reassemble the handshake")
		}
		if bytes.Contains(writes[0][5:], []byte(sni)) || bytes.Contains(writes[1][5:], []byte(sni)) {
			t.Fatalf("tls-record: SNI wholly contained in one record")
		}
	}
}
