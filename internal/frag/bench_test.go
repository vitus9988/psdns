package frag

import (
	"io"
	"testing"

	"github.com/vitus9988/psdns/internal/config"
)

// BenchmarkWriteFirst measures the per-call cost (time + allocations) of writing
// a representative ClientHello with each fragmentation strategy. It writes to
// io.Discard so only the frag logic — parsing and segment/record framing — is
// measured, not a recording writer. The delay is 0 so no sleep skews the timing.
//
//	go test -bench=BenchmarkWriteFirst -benchmem ./internal/frag/
//
// Expected shape: none ~ a single passthrough write (≈0 allocs); split parses
// the SNI then writes two slices (≈0 allocs); tls-record allocates two new
// records via makeRecord, so its B/op and allocs/op are the highest.
func BenchmarkWriteFirst(b *testing.B) {
	hello := buildClientHello("blocked.example.com")
	for _, st := range []struct {
		name string
		s    config.FragStrategy
	}{
		{"none", config.FragNone},
		{"split", config.FragSplit},
		{"tls-record", config.FragTLSRecord},
	} {
		b.Run(st.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(hello)))
			for i := 0; i < b.N; i++ {
				if err := WriteFirst(io.Discard, hello, st.s, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSNISplitOffset isolates the ClientHello parser used by both the split
// and tls-record strategies to locate the SNI host name. It is the dominant CPU
// cost of fragmentation, so tracking it separately makes regressions obvious.
func BenchmarkSNISplitOffset(b *testing.B) {
	hello := buildClientHello("blocked.example.com")
	b.ReportAllocs()
	b.SetBytes(int64(len(hello)))
	for i := 0; i < b.N; i++ {
		if _, ok := sniSplitOffset(hello); !ok {
			b.Fatal("sniSplitOffset failed to locate SNI")
		}
	}
}
