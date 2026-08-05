package supervisor

import (
	"net"
	"testing"
)

// TestListenTCPFallback occupies the requested port and asserts the helper binds
// a different one (the OS-assigned last resort for a non-default port).
func TestListenTCPFallback(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	defer func() { _ = occupied.Close() }()

	ln, err := listenTCPFallback(occupied.Addr().String())
	if err != nil {
		t.Fatalf("listenTCPFallback: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if ln.Addr().String() == occupied.Addr().String() {
		t.Fatalf("expected a different bound port, got the occupied one %s", ln.Addr())
	}
}

func TestFellBack(t *testing.T) {
	cases := []struct {
		requested, bound string
		want             bool
	}{
		{"127.0.0.1:8080", "127.0.0.1:8081", true},
		{"127.0.0.1:8080", "127.0.0.1:8080", false},
		{"127.0.0.1:0", "127.0.0.1:54321", false}, // "any port" request never counts
		{"bogus", "127.0.0.1:8080", false},
	}
	for _, tc := range cases {
		if got := fellBack(tc.requested, tc.bound); got != tc.want {
			t.Fatalf("fellBack(%q,%q) = %v, want %v", tc.requested, tc.bound, got, tc.want)
		}
	}
}
