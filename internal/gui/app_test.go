package gui

import (
	"context"
	"testing"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/supervisor"
)

// TestShouldPreventClose covers the window-close decision in isolation from the
// Wails runtime: a fresh app intercepts the close and hides to the tray, and
// once a real quit is in flight (quitting set, as Quit does) the close is
// allowed through so the app can exit.
func TestShouldPreventClose(t *testing.T) {
	a := &App{}
	if !a.shouldPreventClose() {
		t.Fatal("fresh app should prevent close (hide to tray)")
	}
	a.quitting.Store(true)
	if a.shouldPreventClose() {
		t.Fatal("after quitting is set, close should be allowed through")
	}
}

func TestRuntimeContextHelpers(t *testing.T) {
	a := &App{}
	ctx := context.Background()
	a.setRuntimeContext(ctx)
	if got := a.runtimeContext(); got != ctx {
		t.Fatal("runtimeContext did not return the stored context")
	}
	a.setRuntimeContext(nil)
	if got := a.runtimeContext(); got != nil {
		t.Fatal("runtimeContext should return nil after clearing")
	}
}

// TestMaybeApplySystemProxySkipsWhenNotRunning guards the Start/Stop race: Wails
// may dispatch a Stop on another goroutine during Start's settle delay, tearing
// the servers down before maybeApplySystemProxy runs. The settled State captured
// by Start still shows a live HTTP listener, but the supervisor is no longer
// running — applying now would point the OS proxy at a dead listener that this
// session would never restore. The guard must skip the apply (leave sysproxyOn
// false and never reach sysproxy.Apply, so the OS is untouched).
func TestMaybeApplySystemProxySkipsWhenNotRunning(t *testing.T) {
	a := &App{sup: supervisor.New(config.Default())}
	// State as captured when Start settled (Running true, HTTP up), but the live
	// supervisor was never started / already stopped, so a.sup.Status().Running
	// is false.
	st := supervisor.State{
		Running: true,
		Listeners: []supervisor.Listener{
			{Kind: supervisor.KindHTTP, Addr: "127.0.0.1:8080", Up: true},
		},
	}
	a.maybeApplySystemProxy(st)
	if a.sysproxyOn {
		t.Fatal("must not mark the system proxy applied when the supervisor is not running")
	}
}

func TestCancelBackgroundCheckClearsAndRunsOnce(t *testing.T) {
	a := &App{}
	called := 0
	a.setBackgroundCancel(func() { called++ })
	a.cancelBackgroundCheck()
	a.cancelBackgroundCheck()
	if called != 1 {
		t.Fatalf("cancel called %d times, want 1", called)
	}
}
