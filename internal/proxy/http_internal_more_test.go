package proxy

import (
	"bufio"
	"bytes"
	"net/http"
	"testing"
)

// TestReadAddrTruncatedVariants covers the remaining short-read failures per
// address type (the IPv4 case is covered by TestReadAddrTruncated).
func TestReadAddrTruncatedVariants(t *testing.T) {
	cases := []struct {
		name string
		atyp byte
		data []byte
	}{
		{"domain missing length byte", 0x03, nil},
		{"domain body shorter than length", 0x03, []byte{5, 'a', 'b'}},
		{"IPv6 shorter than 16 bytes", 0x04, []byte{0x20, 0x01, 0x0d}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br := bufio.NewReader(bytes.NewReader(tc.data))
			if host, ok := readAddr(br, tc.atyp); ok {
				t.Fatalf("readAddr = %q,true, want failure", host)
			}
		})
	}
}

// TestIsUpgradeRequest verifies the Connection header must actually name the
// Upgrade token: an Upgrade header alone is not a protocol switch.
func TestIsUpgradeRequest(t *testing.T) {
	cases := []struct {
		name       string
		upgrade    string
		connection string
		want       bool
	}{
		{"no upgrade header", "", "Upgrade", false},
		{"upgrade without connection token", "websocket", "keep-alive", false},
		{"upgrade with connection token", "websocket", "Upgrade", true},
		{"token amid connection list", "websocket", "keep-alive, Upgrade", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &http.Request{Header: http.Header{}}
			if tc.upgrade != "" {
				req.Header.Set("Upgrade", tc.upgrade)
			}
			if tc.connection != "" {
				req.Header.Set("Connection", tc.connection)
			}
			if got := isUpgradeRequest(req); got != tc.want {
				t.Fatalf("isUpgradeRequest = %v, want %v", got, tc.want)
			}
		})
	}
}
