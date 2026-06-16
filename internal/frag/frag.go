// Package frag writes the first client→server payload (the TLS ClientHello) to
// the upstream in a way that prevents on-path DPI from reading the plaintext
// SNI, while remaining valid TLS that the destination server accepts.
//
// Two userspace strategies are provided, both relying only on ordinary socket
// writes (no raw sockets / TTL tricks), so they behave identically on Windows,
// macOS and Linux:
//
//   - split:      emit the ClientHello as multiple TCP segments, cutting inside
//     the SNI host name when it can be located.
//   - tls-record: re-frame the handshake into multiple TLS records (RFC 8446
//     §5.1 allows a handshake message to span records).
//
// The caller is responsible for enabling TCP_NODELAY on the upstream socket so
// each Write becomes its own segment.
package frag

import (
	"io"
	"time"

	"github.com/vitus9988/psdns/internal/config"
)

// WriteFirst writes data (the first client payload) to w using the given
// strategy. Non-TLS first payloads are written unmodified.
func WriteFirst(w io.Writer, data []byte, strategy config.FragStrategy, delay time.Duration) error {
	switch strategy {
	case config.FragSplit:
		return writeSplit(w, data, delay)
	case config.FragTLSRecord:
		return writeTLSRecords(w, data, delay)
	default: // FragNone or unknown
		_, err := w.Write(data)
		return err
	}
}

func writeSplit(w io.Writer, data []byte, delay time.Duration) error {
	off, ok := sniSplitOffset(data)
	if !ok || off <= 0 || off >= len(data) {
		off = fallbackOffset(len(data))
	}
	if off <= 0 || off >= len(data) {
		_, err := w.Write(data)
		return err
	}
	if _, err := w.Write(data[:off]); err != nil {
		return err
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	_, err := w.Write(data[off:])
	return err
}

// fallbackOffset splits just past the 5-byte TLS record header when the SNI
// could not be parsed; this still tends to break naive reassembly.
func fallbackOffset(n int) int {
	switch {
	case n > 6:
		return 6
	case n > 1:
		return 1
	default:
		return 0
	}
}

// writeTLSRecords re-frames a single TLS handshake record into two records,
// splitting inside the SNI when possible.
func writeTLSRecords(w io.Writer, data []byte, delay time.Duration) error {
	if len(data) < 6 || data[0] != 0x16 {
		_, err := w.Write(data)
		return err
	}
	ver0, ver1 := data[1], data[2]
	payload := data[5:]

	sp := len(payload) / 2
	if off, ok := sniSplitOffset(data); ok {
		if po := off - 5; po > 0 && po < len(payload) {
			sp = po
		}
	}
	if sp <= 0 || sp >= len(payload) {
		_, err := w.Write(data)
		return err
	}

	if _, err := w.Write(makeRecord(ver0, ver1, payload[:sp])); err != nil {
		return err
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	_, err := w.Write(makeRecord(ver0, ver1, payload[sp:]))
	return err
}

// makeRecord wraps payload in a TLS handshake record (content type 0x16).
func makeRecord(ver0, ver1 byte, payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = 0x16
	out[1] = ver0
	out[2] = ver1
	out[3] = byte(len(payload) >> 8)
	out[4] = byte(len(payload))
	copy(out[5:], payload)
	return out
}
