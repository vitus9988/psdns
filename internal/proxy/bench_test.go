package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
)

// BenchmarkReadFirstRecord measures the per-connection cost of framing the first
// TLS record (the ClientHello) that relay reads in full before fragmenting it.
// It reads from an in-memory reader so only the framing/parse path is measured.
//
//	go test -bench=BenchmarkReadFirstRecord -benchmem ./internal/proxy/
func BenchmarkReadFirstRecord(b *testing.B) {
	const bodyLen = 512 // a typical ClientHello size
	rec := tlsRecord(bodyLen, bodyLen)
	src := bytes.NewReader(rec)
	r := bufio.NewReader(src)

	b.ReportAllocs()
	b.SetBytes(int64(len(rec)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src.Reset(rec)
		r.Reset(src)
		if _, err := readFirstRecord(r); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRelayThroughput measures the steady-state cost of the relay copy loop
// using in-memory pipes (no kernel/network), with fragmentation disabled so the
// ClientHello-split cost (covered by the frag benchmarks) does not skew it. Each
// iteration streams one chunk upstream→client and tears the relay down. The
// reported MB/s is an in-memory relative figure, not a network throughput.
//
//	go test -bench=BenchmarkRelayThroughput -benchmem ./internal/proxy/
func BenchmarkRelayThroughput(b *testing.B) {
	const chunk = 32 * 1024
	payload := bytes.Repeat([]byte{0xAB}, chunk)
	cfg := config.Default()
	cfg.Frag = config.FragNone
	cfg.Timeout = 2 * time.Second

	b.ReportAllocs()
	b.SetBytes(chunk)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		proxyClient, extClient := net.Pipe()
		proxyUp, extUp := net.Pipe()

		go relay(proxyClient, proxyClient, proxyUp, cfg)
		// Upstream sends one chunk then closes, ending the upstream→client copy.
		go func() {
			_, _ = extUp.Write(payload)
			_ = extUp.Close()
		}()
		// Client drains the relayed chunk; the read returns once relay closes its
		// side (closeBoth) after the upstream EOF.
		_, _ = io.Copy(io.Discard, extClient)
		_ = extClient.Close()
	}
}
