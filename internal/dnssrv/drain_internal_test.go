package dnssrv

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// hangingExchanger stands in for a DoH upstream that never answers, which is what
// an in-flight query looks like while its 10s timeout runs down.
type hangingExchanger struct {
	entered chan struct{}
	release chan struct{}
}

func (h *hangingExchanger) Exchange(ctx context.Context, _ *dns.Msg) (*dns.Msg, error) {
	select {
	case h.entered <- struct{}{}:
	default:
	}
	select {
	case <-h.release:
		return nil, errors.New("released")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestShutdownIsBoundedByDrainTimeout: miekg's Shutdown blocks until every
// handler goroutine has returned, so a query stuck on a slow upstream would hold
// a stop for the whole query timeout — on macOS that teardown runs on the main
// thread during a ⌘Q, where it reads as a hung app. The ports are already
// released by then, so the wait is capped.
func TestShutdownIsBoundedByDrainTimeout(t *testing.T) {
	prev := drainTimeout
	drainTimeout = 100 * time.Millisecond
	t.Cleanup(func() { drainTimeout = prev })

	h := &hangingExchanger{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(h.release)

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()

	srv := New(h, addr, 30*time.Second) // long query timeout: only the cap can save us
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	// Get one query in flight and parked inside the exchanger.
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	cl := &dns.Client{Net: "udp", Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		go func() { _, _, _ = cl.Exchange(q, addr) }()
		select {
		case <-h.entered:
			deadline = time.Time{} // reached the handler
		case <-time.After(100 * time.Millisecond):
			continue
		}
		break
	}
	if !deadline.IsZero() {
		t.Fatal("no query ever reached the handler")
	}

	start := time.Now()
	if err := srv.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Shutdown took %v with a stuck handler: the drain wait is not bounded", elapsed)
	}
	select {
	case <-serveErr:
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return")
	}
	// Whatever the handler is doing, the port must be free.
	if pc, err := net.ListenPacket("udp", addr); err != nil {
		t.Fatalf("port not released: %v", err)
	} else {
		_ = pc.Close()
	}
}
