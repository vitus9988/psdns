package gui

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"slices"
	"testing"

	"github.com/vitus9988/psdns/internal/supervisor"
	"github.com/vitus9988/psdns/internal/sysproxy"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// recordEvents swaps the wails event seam for a recorder so tests can assert
// which UI events a path emitted, without a live Wails runtime.
func recordEvents(t *testing.T) *[]string {
	t.Helper()
	var events []string
	prev := wailsEventsEmit
	wailsEventsEmit = func(_ context.Context, name string, _ ...interface{}) {
		events = append(events, name)
	}
	t.Cleanup(func() { wailsEventsEmit = prev })
	return &events
}

// TestBeforeCloseHidesToTray covers the hide path: with no quit in flight, the
// window's X must be intercepted and the window hidden instead of closed.
func TestBeforeCloseHidesToTray(t *testing.T) {
	hidden := false
	prev := wailsWindowHide
	wailsWindowHide = func(context.Context) { hidden = true }
	t.Cleanup(func() { wailsWindowHide = prev })

	a := &App{}
	if !a.BeforeClose(context.Background()) {
		t.Fatal("BeforeClose must prevent the close and hide to the tray")
	}
	if !hidden {
		t.Fatal("BeforeClose must hide the window")
	}
}

// TestWindowRevealPaths covers OnSecondInstance and showWindow with a stored
// runtime context: both must run the full reveal sequence.
func TestWindowRevealPaths(t *testing.T) {
	var calls []string
	prevWShow, prevUnmin, prevShow := wailsWindowShow, wailsWindowUnminimise, wailsShow
	wailsWindowShow = func(context.Context) { calls = append(calls, "windowShow") }
	wailsWindowUnminimise = func(context.Context) { calls = append(calls, "unminimise") }
	wailsShow = func(context.Context) { calls = append(calls, "show") }
	t.Cleanup(func() {
		wailsWindowShow, wailsWindowUnminimise, wailsShow = prevWShow, prevUnmin, prevShow
	})

	// Both paths must run the full reveal sequence, WindowShow first: after a
	// hide-to-tray (BeforeClose → WindowHide) a relaunch (OnSecondInstance) must
	// undo the hide, not just Unminimise+Show, or the window never comes back.
	full := []string{"windowShow", "unminimise", "show"}

	a := &App{}
	a.setRuntimeContext(context.Background())
	a.OnSecondInstance(options.SecondInstanceData{})
	if !slices.Equal(calls, full) {
		t.Fatalf("OnSecondInstance calls = %v, want %v", calls, full)
	}

	calls = nil
	a.showWindow()
	if !slices.Equal(calls, full) {
		t.Fatalf("showWindow calls = %v, want %v", calls, full)
	}
}

// TestEmitSysProxyWithContext covers the emitting branch of emitSysProxy.
func TestEmitSysProxyWithContext(t *testing.T) {
	events := recordEvents(t)
	a := &App{}
	a.setRuntimeContext(context.Background())
	a.emitSysProxy("applied", "ok")
	if want := []string{"sysproxy:applied"}; !slices.Equal(*events, want) {
		t.Fatalf("events = %v, want %v", *events, want)
	}
}

// TestQuitClosesViaRuntime covers Quit's final step: with a live runtime
// context, the app must be told to quit.
func TestQuitClosesViaRuntime(t *testing.T) {
	redirectConfigDir(t)
	quitCalled := false
	prev := wailsQuit
	wailsQuit = func(context.Context) { quitCalled = true }
	t.Cleanup(func() { wailsQuit = prev })

	a := NewApp("t")
	a.setRuntimeContext(context.Background())
	a.Quit()
	if !quitCalled {
		t.Fatal("Quit must close the app through the runtime")
	}
}

// TestMaybeApplySystemProxyUnsupported covers the unsupported-platform early
// return: nothing is applied and nothing is owed.
func TestMaybeApplySystemProxyUnsupported(t *testing.T) {
	prev := sysproxySupported
	sysproxySupported = func() bool { return false }
	t.Cleanup(func() { sysproxySupported = prev })

	a := NewApp("t") // SetSystemProxy is on by default
	a.maybeApplySystemProxy(newHTTPUpState())
	if a.sysproxyOn {
		t.Fatal("must not owe a restore on an unsupported platform")
	}
}

// TestStartStopAppliesAndRestoresSystemProxy drives the full Start→apply→Stop→
// restore sequence through the seams: an apply failure still owes a restore
// (the backup is written before the OS is touched), a successful apply emits
// the applied toast, and Stop restores exactly once.
func TestStartStopAppliesAndRestoresSystemProxy(t *testing.T) {
	redirectConfigDir(t)
	events := recordEvents(t)
	prevSup, prevApply, prevRestore, prevCur := sysproxySupported, sysproxyApply, sysproxyRestore, sysproxyCurrent
	t.Cleanup(func() {
		sysproxySupported, sysproxyApply, sysproxyRestore, sysproxyCurrent = prevSup, prevApply, prevRestore, prevCur
	})
	sysproxySupported = func() bool { return true }
	// No conflicting local proxy, so the apply path proceeds (and stays hermetic —
	// the default would shell out to the OS to read the real proxy state).
	sysproxyCurrent = func() (sysproxy.DetectedProxy, error) { return sysproxy.DetectedProxy{}, nil }
	restored := 0
	sysproxyRestore = func() error { restored++; return nil }

	newApp := func(t *testing.T) *App {
		a := NewApp("t")
		a.setRuntimeContext(context.Background())
		u := a.GetConfig() // SetSystemProxy stays on (the default)
		u.ProxyListen, u.SocksListen = "127.0.0.1:0", "127.0.0.1:0"
		if _, err := a.SetConfig(u); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		return a
	}

	// Apply fails → the error toast is emitted and the restore is still owed.
	sysproxyApply = func(sysproxy.Settings) error { return errors.New("refused") }
	a := newApp(t)
	if _, err := a.Start("proxy"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !a.sysproxyOn {
		t.Fatal("a failed apply must still owe a restore")
	}
	if !slices.Contains(*events, "sysproxy:error") {
		t.Fatalf("want sysproxy:error toast, got %v", *events)
	}
	if _, err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if a.sysproxyOn || restored != 1 {
		t.Fatalf("Stop must restore exactly once (restored=%d, owed=%v)", restored, a.sysproxyOn)
	}

	// Apply succeeds → the applied toast is emitted, Stop restores again.
	*events = nil
	sysproxyApply = func(sysproxy.Settings) error { return nil }
	b := newApp(t)
	if _, err := b.Start("proxy"); err != nil {
		t.Fatalf("Start#2: %v", err)
	}
	if !slices.Contains(*events, "sysproxy:applied") {
		t.Fatalf("want sysproxy:applied toast, got %v", *events)
	}
	if _, err := b.Stop(); err != nil {
		t.Fatalf("Stop#2: %v", err)
	}
	if !slices.Contains(*events, "sysproxy:restored") || restored != 2 {
		t.Fatalf("want sysproxy:restored toast (restored=%d), got %v", restored, *events)
	}
}

// TestStartSkipsSystemProxyOnConflict covers the AdGuard-conflict guard: when the
// OS already routes web traffic through a *different* loopback proxy, psdns must
// not overwrite it — Apply is never called, no restore is owed, and a conflict
// toast tells the user why.
func TestStartSkipsSystemProxyOnConflict(t *testing.T) {
	redirectConfigDir(t)
	events := recordEvents(t)
	prevSup, prevApply, prevRestore, prevCur := sysproxySupported, sysproxyApply, sysproxyRestore, sysproxyCurrent
	t.Cleanup(func() {
		sysproxySupported, sysproxyApply, sysproxyRestore, sysproxyCurrent = prevSup, prevApply, prevRestore, prevCur
	})
	sysproxySupported = func() bool { return true }
	applied := false
	sysproxyApply = func(sysproxy.Settings) error { applied = true; return nil }
	sysproxyRestore = func() error { return nil }
	// Another local filtering proxy (e.g. AdGuard on 127.0.0.1:3128) is already set.
	sysproxyCurrent = func() (sysproxy.DetectedProxy, error) {
		return sysproxy.DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 3128}, nil
	}

	a := NewApp("t")
	a.setRuntimeContext(context.Background())
	u := a.GetConfig() // SetSystemProxy stays on (the default)
	u.ProxyListen, u.SocksListen = "127.0.0.1:0", "127.0.0.1:0"
	if _, err := a.SetConfig(u); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := a.Start("proxy"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _, _ = a.Stop() })

	if applied {
		t.Fatal("must not overwrite an existing local proxy")
	}
	if a.sysproxyOn {
		t.Fatal("a skipped apply must not owe a restore")
	}
	if !slices.Contains(*events, "sysproxy:conflict") {
		t.Fatalf("want sysproxy:conflict toast, got %v", *events)
	}
}

// TestRestoreSystemProxyFailureKeepsOwed covers the restore-failure branch: the
// flag must stay set so a later call retries.
func TestRestoreSystemProxyFailureKeepsOwed(t *testing.T) {
	events := recordEvents(t)
	prev := sysproxyRestore
	sysproxyRestore = func() error { return errors.New("boom") }
	t.Cleanup(func() { sysproxyRestore = prev })

	a := &App{sysproxyOn: true}
	a.setRuntimeContext(context.Background())
	a.restoreSystemProxy()
	if !a.sysproxyOn {
		t.Fatal("a failed restore must keep the flag set for a retry")
	}
	if !slices.Contains(*events, "sysproxy:error") {
		t.Fatalf("want sysproxy:error toast, got %v", *events)
	}
}

// TestStartupLogsStaleCleanupFailure covers Startup's stale-cleanup error
// branch: the failure is logged and startup continues.
func TestStartupLogsStaleCleanupFailure(t *testing.T) {
	redirectConfigDir(t)
	prev := sysproxyRecoverStale
	sysproxyRecoverStale = func() (bool, error) { return false, errors.New("corrupt backup") }
	t.Cleanup(func() { sysproxyRecoverStale = prev })

	a := NewApp("t")
	a.updater = stubErrChecker()
	a.Startup(context.Background())
	a.trayEnd = func() {} // never run the real external-loop end hook
	a.Shutdown(context.Background())
}

// TestApplyUpdateEmitsProgress covers the progress-event emit: the StageStart
// callback fires before the (failing) network fetch.
func TestApplyUpdateEmitsProgress(t *testing.T) {
	events := recordEvents(t)
	a := &App{updater: stubErrChecker()}
	a.setRuntimeContext(context.Background())
	if err := a.ApplyUpdate(); err == nil {
		t.Fatal("ApplyUpdate must surface the failing fetch")
	}
	if !slices.Contains(*events, "update:progress") {
		t.Fatalf("want an update:progress event before the fetch, got %v", *events)
	}
}

// TestStopTraySystrayQuit covers the Windows-shaped teardown: with no
// external-loop end hook, stopTray must go through systray.Quit.
func TestStopTraySystrayQuit(t *testing.T) {
	called := false
	prev := systrayQuit
	systrayQuit = func() { called = true }
	t.Cleanup(func() { systrayQuit = prev })

	a := &App{} // trayEnd nil, as on Windows
	a.stopTray()
	if !called {
		t.Fatal("stopTray must call systray.Quit when no end hook is set")
	}
}

// TestApplyTrayIconUsesPlatformFormat covers applyTrayIcon: .ico on Windows,
// .png elsewhere.
func TestApplyTrayIconUsesPlatformFormat(t *testing.T) {
	var got []byte
	prev := systraySetIcon
	systraySetIcon = func(b []byte) { got = b }
	t.Cleanup(func() { systraySetIcon = prev })

	applyTrayIcon()
	want := trayPNG
	if runtime.GOOS == "windows" {
		want = trayICO
	}
	if !bytes.Equal(got, want) {
		t.Fatal("applyTrayIcon must pass the platform-appropriate icon bytes")
	}
}

// newHTTPUpState builds a settled state with a live HTTP listener, the shape
// maybeApplySystemProxy acts on.
func newHTTPUpState() supervisor.State {
	return supervisor.State{
		Running: true,
		Listeners: []supervisor.Listener{
			{Kind: supervisor.KindHTTP, Addr: "127.0.0.1:8080", Up: true},
		},
	}
}
