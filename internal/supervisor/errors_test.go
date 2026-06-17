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
		name  string
		err   error
		match string
	}{
		{"permission", syscall.EACCES, "권한"},
		{"in use", syscall.EADDRINUSE, "사용 중"},
		{"other", errors.New("some other failure"), "실패"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyBindErr("DNS", "127.0.0.1:53", tc.err)
			if !strings.Contains(got, tc.match) {
				t.Fatalf("classifyBindErr(%v) = %q, want substring %q", tc.err, got, tc.match)
			}
		})
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
