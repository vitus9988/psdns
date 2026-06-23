package proxy_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestHTTPConnectFragmentationScenarios drives the full HTTP CONNECT path for a
// range of SNI shapes and asserts the upstream always sees the ClientHello
// fragmented with the SNI host name straddling a segment boundary. It is the
// integration counterpart to the byte-level scenarios in the frag package: it
// guards against the relay mishandling short or long ClientHellos (e.g. reading
// a partial first record) where the unit tests would not notice.
func TestHTTPConnectFragmentationScenarios(t *testing.T) {
	cases := []struct{ name, sni string }{
		{"short", "x.io"},
		{"typical", "blocked.example.com"},
		// Long but DNS-valid: labels must stay <=63 octets so the resolver can
		// pack the query (the host is resolved over the mock DoH before dialing).
		{"long", strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + ".example.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const greeting = "UPSTREAM-GREETING"
			upstream := newRecordingUpstream(t, greeting)
			res := mockResolver(t, "127.0.0.1")
			addr := startHTTP(t, res)

			conn := waitListen(t, addr)
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

			target := net.JoinHostPort(tc.sni, upstream.port())
			fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)

			br := bufio.NewReader(conn)
			resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
			if err != nil {
				t.Fatalf("read CONNECT response: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
			}

			hello := buildClientHello(tc.sni)
			if _, err := conn.Write(hello); err != nil {
				t.Fatalf("write ClientHello: %v", err)
			}
			got := make([]byte, len(greeting))
			if _, err := io.ReadFull(br, got); err != nil {
				t.Fatalf("read relayed greeting: %v", err)
			}

			assertFragmented(t, upstream, hello, tc.sni)
		})
	}
}
