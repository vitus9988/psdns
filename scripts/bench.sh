#!/usr/bin/env bash
# Reproducible micro-benchmarks for the hot paths: ClientHello fragmentation
# (internal/frag), the resolver cache hit (internal/resolver) and the proxy's
# first-record framing / relay copy (internal/proxy). Prints ns/op, B/op and
# allocs/op so the numbers in docs/measurements.md can be regenerated on any
# machine.
#
# Usage:
#   scripts/bench.sh                 # run all benchmarks
#   scripts/bench.sh -cpuprofile     # profile internal/frag -> cpu.prof, mem.prof
#
# CPU profiling is restricted to a single package (Go forbids -cpuprofile across
# multiple packages); internal/frag is the most CPU-bound, so it is profiled.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "go version: $(go version)"
echo "GOOS=${GOOS:-$(go env GOOS)} GOARCH=${GOARCH:-$(go env GOARCH)}"
echo

if [[ "${1:-}" == "-cpuprofile" ]]; then
	echo "profiling ./internal/frag (writing cpu.prof, mem.prof)"
	go test -run='^$' -bench=. -benchmem -benchtime=3s \
		-cpuprofile=cpu.prof -memprofile=mem.prof ./internal/frag/
	echo
	echo "inspect with: go tool pprof cpu.prof   (or)   go tool pprof mem.prof"
	exit 0
fi

go test -run='^$' -bench=. -benchmem -benchtime=2s \
	./internal/frag/ ./internal/resolver/ ./internal/proxy/
