//go:build !windows

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// The happy-path subcommand tests shut the blocking servers down by delivering
// a real SIGTERM to this process, which has no Windows equivalent
// (syscall.Kill does not exist there), so they are unix-only.

func sigterm(t *testing.T) {
	t.Helper()
	// Signal only this process; signal.Notify inside the subcommand intercepts
	// SIGTERM, so the test binary is not killed.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
}

func TestRunResolveHappyPath(t *testing.T) {
	muteLog(t)
	stubFatals(t)
	exited := stubExitSignal(t)

	done := runInBackground(func() { runResolve([]string{"-listen", "127.0.0.1:0"}) })

	// Let the UDP/TCP listeners bind and onSignal register its handler.
	time.Sleep(500 * time.Millisecond)
	sigterm(t)

	awaitClose(t, done, "runResolve did not unwind after SIGTERM")
	awaitExit(t, exited, 0)
}

func TestRunProxyHappyPath(t *testing.T) {
	// A release build + a newer fake release makes the background update check
	// log its hint, which waitUpdateNoticed observes.
	setBuildVersion(t, "v1.0.0")
	fakeReleaseAPI(t, "v9.9.9", true)
	noticed := waitUpdateNoticed(t)
	stubFatals(t)
	exited := stubExitSignal(t)

	done := runInBackground(func() {
		runProxy([]string{"-http", "127.0.0.1:0", "-socks", "127.0.0.1:0"})
	})

	// Let the listeners bind and onSignal register.
	time.Sleep(500 * time.Millisecond)
	select {
	case <-noticed:
	case <-time.After(5 * time.Second):
		t.Fatal("notifyUpdate never logged the newer-version hint")
	}
	sigterm(t)

	awaitClose(t, done, "runProxy did not unwind after SIGTERM")
	awaitExit(t, exited, 0)
}

func TestRunAllHappyPath(t *testing.T) {
	setBuildVersion(t, "v1.0.0")
	fakeReleaseAPI(t, "v9.9.9", true)
	noticed := waitUpdateNoticed(t)
	stubFatals(t)
	exited := stubExitSignal(t)

	done := runInBackground(func() {
		runAll([]string{"-dns", "127.0.0.1:0", "-http", "127.0.0.1:0", "-socks", "127.0.0.1:0"})
	})

	time.Sleep(500 * time.Millisecond)
	select {
	case <-noticed:
	case <-time.After(5 * time.Second):
		t.Fatal("notifyUpdate never logged the newer-version hint")
	}
	sigterm(t)

	awaitClose(t, done, "runAll did not unwind after SIGTERM")
	awaitExit(t, exited, 0)
}
