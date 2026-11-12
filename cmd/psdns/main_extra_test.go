package main

import (
	"log"
	"net"
	"os"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
)

// setArgs swaps os.Args for an in-process main() dispatch test.
func setArgs(t *testing.T, args ...string) {
	t.Helper()
	prev := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = prev })
}

func TestMainVersion(t *testing.T) {
	muteStdio(t)
	setArgs(t, "psdns", "version")
	main()
}

func TestMainHelp(t *testing.T) {
	muteStdio(t)
	setArgs(t, "psdns", "help")
	main()
}

func TestMustDoH(t *testing.T) {
	c := config.Default()
	if client := mustDoH(&c); client == nil {
		t.Fatal("mustDoH returned nil for the default config")
	}
}

func TestMaybeStartPprofOff(t *testing.T) {
	prev := pprofAddr
	pprofAddr = ""
	t.Cleanup(func() { pprofAddr = prev })
	// Without -pprof this must be a no-op (no listener, no goroutine).
	maybeStartPprof()
}

// writerFunc adapts a function to io.Writer for capturing log output.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestMaybeStartPprofOn(t *testing.T) {
	// Occupy a port so the background ListenAndServe fails fast and the
	// goroutine terminates within the test instead of leaking a live server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	logged := make(chan struct{}, 4)
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		logged <- struct{}{}
		return len(p), nil
	}))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	prev := pprofAddr
	pprofAddr = ln.Addr().String()
	maybeStartPprof()
	// The goroutine logs twice: "serving on" (which reads pprofAddr) and the
	// bind error. Receiving both gives the happens-before edge required to
	// write pprofAddr back without racing that read.
	for i := 0; i < 2; i++ {
		select {
		case <-logged:
		case <-time.After(5 * time.Second):
			t.Fatal("pprof goroutine never logged its startup/error lines")
		}
	}
	pprofAddr = prev
}
