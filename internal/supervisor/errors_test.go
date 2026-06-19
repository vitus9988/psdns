package supervisor

import (
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
)

func TestClassifyBindErr(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		addr    string
		err     error
		match   string
		exclude string // substring that must NOT appear
	}{
		{"privileged permission", "DNS", "127.0.0.1:53", syscall.EACCES, "관리자", ""},
		{"high-port permission", "http", "127.0.0.1:8080", syscall.EACCES, "예약", "53번"},
		{"in use", "http", "127.0.0.1:8080", syscall.EADDRINUSE, "사용 중", ""},
		{"other", "http", "127.0.0.1:8080", errors.New("some other failure"), "실패", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyBindErr(tc.kind, tc.addr, tc.err)
			if !strings.Contains(got, tc.match) {
				t.Fatalf("classifyBindErr(%v) = %q, want substring %q", tc.err, got, tc.match)
			}
			if tc.exclude != "" && strings.Contains(got, tc.exclude) {
				t.Fatalf("classifyBindErr(%v) = %q, must not contain %q", tc.err, got, tc.exclude)
			}
		})
	}
}

// TestUnwrapListenErr verifies the redundant "listen tcp <addr>:" wrapper is
// stripped so the friendly message does not repeat the address.
func TestUnwrapListenErr(t *testing.T) {
	wrapped := &net.OpError{Op: "listen", Net: "tcp", Err: errors.New("bind: cannot assign")}
	got := classifyBindErr("http", "127.0.0.1:8080", wrapped)
	if strings.Count(got, "127.0.0.1:8080") != 1 {
		t.Fatalf("address should appear exactly once, got %q", got)
	}
}

func TestIsClosed(t *testing.T) {
	if !isClosed(net.ErrClosed) {
		t.Fatalf("isClosed(net.ErrClosed) = false, want true")
	}
	if isClosed(errors.New("nope")) {
		t.Fatalf("isClosed(other) = true, want false")
	}
}
