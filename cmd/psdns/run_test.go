package main

import (
	"bytes"
	"fmt"
	"log"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
)

// fatalExit is the sentinel a stubbed logFatal/logFatalf panics with. Production
// log.Fatal/log.Fatalf never return, so the seams must not either; panicking
// with this type lets a test distinguish the intended "would have exited here"
// from an unexpected panic (which is re-raised).
type fatalExit struct{ msg string }

// stubFatals overrides logFatal/logFatalf with panicking stand-ins and restores
// them on cleanup. Read of the seams by the code under test must be
// happens-before this cleanup (achieved via the done/exited channels below), so
// no -race report fires on the restore.
func stubFatals(t *testing.T) {
	t.Helper()
	pf, pff := logFatal, logFatalf
	logFatal = func(v ...any) { panic(fatalExit{fmt.Sprint(v...)}) }
	logFatalf = func(format string, v ...any) { panic(fatalExit{fmt.Sprintf(format, v...)}) }
	t.Cleanup(func() {
		logFatal = pf
		logFatalf = pff
	})
}

// stubExit overrides osExit to record the code and return normally (main's exit
// call sites tolerate a normal return). It returns a pointer to the last code.
func stubExit(t *testing.T) *int {
	t.Helper()
	code := -1
	pe := osExit
	osExit = func(c int) { code = c }
	t.Cleanup(func() { osExit = pe })
	return &code
}

// stubExitSignal overrides osExit to publish the code on a buffered channel. The
// channel receive gives the test a happens-before edge to onSignal's osExit
// call, so restoring the seam on cleanup does not race the (now-finished)
// signal goroutine.
func stubExitSignal(t *testing.T) <-chan int {
	t.Helper()
	ch := make(chan int, 4)
	pe := osExit
	osExit = func(c int) { ch <- c }
	t.Cleanup(func() { osExit = pe })
	return ch
}

// expectFatal is deferred by tests that expect the code under test to hit a
// stubbed fatal seam. A normal return fails the test; an unexpected panic is
// re-raised.
func expectFatal(t *testing.T) {
	t.Helper()
	switch r := recover().(type) {
	case nil:
		t.Error("expected a stubbed fatal panic, but the call returned normally")
	case fatalExit:
		// expected
	default:
		panic(r)
	}
}

// waitUpdateNoticed points the default logger at a writer that signals when the
// background update check logs its newer-version hint. Waiting on the returned
// channel proves notifyUpdate ran (and thus read apiBase) before teardown.
func waitUpdateNoticed(t *testing.T) <-chan struct{} {
	t.Helper()
	noticed := make(chan struct{}, 1)
	prev := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		if bytes.Contains(p, []byte("새 버전")) {
			select {
			case noticed <- struct{}{}:
			default:
			}
		}
		return len(p), nil
	}))
	t.Cleanup(func() { log.SetOutput(prev) })
	return noticed
}

// runInBackground launches a blocking subcommand and returns a channel closed
// when it unwinds — either by returning normally (clean shutdown) or via a
// recovered stubbed-fatal panic. A non-sentinel panic is re-raised so a real
// bug crashes the test instead of being swallowed.
func runInBackground(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fatalExit); !ok {
					panic(r)
				}
			}
			close(done)
		}()
		fn()
	}()
	return done
}

func awaitClose(t *testing.T, done <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
}

func awaitExit(t *testing.T, exited <-chan int, want int) {
	t.Helper()
	select {
	case code := <-exited:
		if code != want {
			t.Errorf("onSignal called osExit(%d), want %d", code, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onSignal never called osExit")
	}
}

func TestDoHSummary(t *testing.T) {
	c := config.Default()
	if got := dohSummary(&c); got != c.DoHURL {
		t.Errorf("dohSummary(no fallbacks) = %q, want %q", got, c.DoHURL)
	}
	c.DoHFallbacks = []string{"https://8.8.8.8/dns-query", "https://9.9.9.9/dns-query"}
	want := c.DoHURL + " (+2 fallback)"
	if got := dohSummary(&c); got != want {
		t.Errorf("dohSummary(2 fallbacks) = %q, want %q", got, want)
	}
}

func TestMustDoHFatal(t *testing.T) {
	stubFatals(t)
	defer expectFatal(t)
	c := config.Default()
	c.DoHURL = "ftp://nope" // rejected scheme -> NewExchanger errors -> logFatalf
	mustDoH(&c)
}

func TestMainNoArgs(t *testing.T) {
	muteStdio(t)
	code := stubExit(t)
	setArgs(t, "psdns")
	main()
	if *code != 2 {
		t.Errorf("main() with no subcommand: osExit(%d), want 2", *code)
	}
}

func TestMainUnknownCommand(t *testing.T) {
	muteStdio(t)
	code := stubExit(t)
	setArgs(t, "psdns", "frobnicate")
	main()
	if *code != 2 {
		t.Errorf("main() with unknown subcommand: osExit(%d), want 2", *code)
	}
}

func TestRunResolveFinalizeError(t *testing.T) {
	stubFatals(t)
	defer expectFatal(t)
	// -frag parses fine (it is a string flag) but finalize rejects the value,
	// so runResolve hits logFatal before opening any listener.
	runResolve([]string{"-listen", "127.0.0.1:0", "-frag", "bogus"})
}

func TestRunProxyFinalizeError(t *testing.T) {
	stubFatals(t)
	defer expectFatal(t)
	runProxy([]string{"-http", "127.0.0.1:0", "-socks", "127.0.0.1:0", "-frag", "bogus"})
}

func TestRunAllFinalizeError(t *testing.T) {
	stubFatals(t)
	defer expectFatal(t)
	runAll([]string{"-dns", "127.0.0.1:0", "-http", "127.0.0.1:0", "-socks", "127.0.0.1:0", "-frag", "bogus"})
}

// TestRunResolveHappyPath / TestRunProxyHappyPath / TestRunAllHappyPath live in
// run_unix_test.go: they stop the blocking servers with a real SIGTERM, which
// has no Windows equivalent.
