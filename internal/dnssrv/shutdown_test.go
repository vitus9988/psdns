package dnssrv_test

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/dnssrv"
)

// rebindable reports whether addr can be bound again on both protocols — i.e.
// the server under test fully released it.
func rebindable(t *testing.T, addr string) error {
	t.Helper()
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	_ = pc.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	_ = ln.Close()
	return nil
}

// TestShutdownRacingListenAndServeReleasesPort stops the server immediately
// after starting it, over many iterations so Shutdown lands both before and
// after miekg marks the server started. Whatever the interleaving, the port
// must be released — a Shutdown that loses the race used to leave the sockets
// bound inside the process forever, so the next start reported the port as in
// use. Run with -race.
func TestShutdownRacingListenAndServeReleasesPort(t *testing.T) {
	addr := freeUDPPort(t)
	mock := newMockDoH(t, "198.51.100.42")
	for i := 0; i < 50; i++ {
		srv := dnssrv.New(mock, addr, time.Second)
		serveErr := make(chan error, 1)
		go func() { serveErr <- srv.ListenAndServe() }()
		if err := srv.Shutdown(); err != nil {
			t.Fatalf("iter %d: Shutdown: %v", i, err)
		}
		select {
		case <-serveErr:
		case <-time.After(3 * time.Second):
			t.Fatalf("iter %d: ListenAndServe did not return after Shutdown", i)
		}
		if err := rebindable(t, addr); err != nil {
			t.Fatalf("iter %d: port not released after Shutdown: %v", i, err)
		}
	}
}

// TestShutdownBeforeListenAndServe verifies a stop that lands before the serve
// call: ListenAndServe must give the sockets straight back and report a clean
// close instead of serving on a server that was already stopped.
func TestShutdownBeforeListenAndServe(t *testing.T) {
	addr := freeUDPPort(t)
	srv := dnssrv.New(newMockDoH(t, "198.51.100.42"), addr, time.Second)
	if err := srv.Shutdown(); err != nil {
		t.Fatalf("Shutdown before serve: %v", err)
	}
	if err := srv.ListenAndServe(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ListenAndServe after Shutdown = %v, want net.ErrClosed", err)
	}
	if err := rebindable(t, addr); err != nil {
		t.Fatalf("port not released: %v", err)
	}
}

// TestShutdownIdempotent: a second Shutdown is a no-op returning nil.
func TestShutdownIdempotent(t *testing.T) {
	addr := freeUDPPort(t)
	srv := dnssrv.New(newMockDoH(t, "198.51.100.42"), addr, time.Second)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	if err := srv.Shutdown(); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	<-serveErr
	if err := srv.Shutdown(); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}
