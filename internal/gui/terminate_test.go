package gui

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/supervisor"
)

// TestTerminationSignalRestoresAndExits covers the SIGTERM path: the OS proxy is
// put back and the servers released before the process exits.
func TestTerminationSignalRestoresAndExits(t *testing.T) {
	recordEvents(t)
	codes := recordExit(t)
	restores := 0
	prev := sysproxyRestore
	sysproxyRestore = func() error { restores++; return nil }
	t.Cleanup(func() { sysproxyRestore = prev })

	a := &App{sup: supervisor.New(config.Default()), sysproxyOn: true}
	a.setRuntimeContext(context.Background())

	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() { a.awaitTerminationSignal(ch); close(done) }()
	ch <- syscall.SIGTERM

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("signal handler did not finish")
	}
	if restores != 1 {
		t.Fatalf("restore ran %d times, want 1", restores)
	}
	if len(*codes) != 1 || (*codes)[0] != 0 {
		t.Fatalf("want a single exit(0), got %v", *codes)
	}
}

// TestTerminationSignalIgnoresClosedChannel: a closed channel (no signal ever
// arrived) must not tear anything down or exit.
func TestTerminationSignalIgnoresClosedChannel(t *testing.T) {
	codes := recordExit(t)
	restores := 0
	prev := sysproxyRestore
	sysproxyRestore = func() error { restores++; return nil }
	t.Cleanup(func() { sysproxyRestore = prev })

	a := &App{sysproxyOn: true}
	ch := make(chan os.Signal)
	close(ch)
	a.awaitTerminationSignal(ch)

	if restores != 0 || len(*codes) != 0 {
		t.Fatalf("closed channel must be inert, got %d restores and exits %v", restores, *codes)
	}
}

// TestShutdownForTerminationRestoresOnce covers the teardown shared by the macOS
// delegate quit (⌘Q / Dock / logout, see tray_darwin.go) and the signal handler:
// it must restore the OS proxy, stop the servers, and be safe to run twice — a
// quit can reach it and then still fall through to Shutdown.
func TestShutdownForTerminationRestoresOnce(t *testing.T) {
	recordEvents(t)
	restores := 0
	prev := sysproxyRestore
	sysproxyRestore = func() error { restores++; return nil }
	t.Cleanup(func() { sysproxyRestore = prev })

	a := &App{sup: supervisor.New(config.Default()), sysproxyOn: true}
	a.setRuntimeContext(context.Background())

	a.shutdownForTermination()
	a.shutdownForTermination()

	if restores != 1 {
		t.Fatalf("restore ran %d times, want exactly 1", restores)
	}
	if !a.quitting.Load() {
		t.Fatal("quitting must be set so a racing close is not turned into a hide")
	}
	if a.sysproxyOn {
		t.Fatal("sysproxyOn must be cleared after a successful restore")
	}
}

// TestShutdownForTerminationStopsServers verifies the servers really are torn
// down — the DNS listener is what would otherwise stay bound to :53 and block
// the next launch.
func TestShutdownForTerminationStopsServers(t *testing.T) {
	recordEvents(t)
	sup := supervisor.New(config.Default())
	if err := sup.Start(supervisor.ModeProxy); err != nil {
		t.Fatalf("Start: %v", err)
	}
	a := &App{sup: sup}
	a.setRuntimeContext(context.Background())

	a.shutdownForTermination()

	if sup.Status().Running {
		t.Fatal("servers must be stopped")
	}
	if err := sup.Stop(); !errors.Is(err, supervisor.ErrNotRunning) {
		t.Fatalf("second Stop = %v, want ErrNotRunning", err)
	}
}

// TestShutdownForTerminationReleasesInstanceLock: the lock must not outlive the
// process's own teardown, or a relaunch would refuse to start.
func TestShutdownForTerminationReleasesInstanceLock(t *testing.T) {
	useTempLock(t)
	a := &App{}
	if !a.acquireInstanceLock() {
		t.Fatal("acquire must succeed")
	}
	a.shutdownForTermination()

	other := &App{}
	if !other.acquireInstanceLock() {
		t.Fatal("lock must be free after termination teardown")
	}
	other.releaseInstanceLock()
}
