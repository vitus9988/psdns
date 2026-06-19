package supervisor

import (
	"net"
	"testing"
)

func TestListenCandidates(t *testing.T) {
	got := listenCandidates("127.0.0.1:8080")
	want := []string{"127.0.0.1:8080", "127.0.0.1:8081", "127.0.0.1:8088", "127.0.0.1:18080", "127.0.0.1:0"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}

	// A custom (non-default) port has no stable alternates: just itself then :0.
	got = listenCandidates("127.0.0.1:9999")
	if len(got) != 2 || got[0] != "127.0.0.1:9999" || got[1] != "127.0.0.1:0" {
		t.Fatalf("custom-port candidates = %v, want [127.0.0.1:9999 127.0.0.1:0]", got)
	}
}

// TestListenTCPFallback occupies the requested port and asserts the helper binds
// a different one (the OS-assigned last resort for a non-default port).
func TestListenTCPFallback(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind: %v", err)
	}
	defer occupied.Close()

	ln, err := listenTCPFallback(occupied.Addr().String())
	if err != nil {
		t.Fatalf("listenTCPFallback: %v", err)
	}
	defer ln.Close()
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
