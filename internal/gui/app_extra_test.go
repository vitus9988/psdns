package gui

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/selfupdate"
	"github.com/vitus9988/psdns/internal/supervisor"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// redirectConfigDir points os.UserConfigDir at a temp dir on every OS, so any
// path that can reach sysproxy.Restore / RecoverStale (Startup, Shutdown, Stop,
// Quit, restoreSystemProxy*) never touches the developer's real psdns config.
// With no backup file present those calls are verified no-ops. Copied from
// internal/sysproxy/backup_test.go.
func redirectConfigDir(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)                                       // darwin (…/Library/Application Support) and unix fallback
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))      // linux
	t.Setenv("AppData", filepath.Join(tmp, "appdata"))          // windows
	t.Setenv("XDG_CONFIG_DIRS", filepath.Join(tmp, "xdg-dirs")) // defensive: never consult system dirs
}

// roundTripFunc adapts a function into an http.RoundTripper so the updater can
// be driven without any real network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubErrChecker returns a Checker whose transport always errors, so every
// updater call (Check/Apply) fails fast without a real GitHub request.
func stubErrChecker() *selfupdate.Checker {
	return selfupdate.NewChecker(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network disabled in test")
	})})
}

// TestCheapAccessors covers the trivial getters and the pure helpers that need
// no runtime.
func TestCheapAccessors(t *testing.T) {
	a := NewApp("v9.9.9")
	if a.Version() != "v9.9.9" {
		t.Fatalf("Version = %q, want v9.9.9", a.Version())
	}
	if a.sup == nil || a.updater == nil {
		t.Fatal("NewApp must wire a supervisor and an updater")
	}
	if a.GetStatus().Running {
		t.Fatal("a fresh app must not be running")
	}
	// GetConfig / SystemProxySupported just marshal state; call them for coverage.
	_ = a.GetConfig()
	_ = a.SystemProxySupported()
	// emitSysProxy is a no-op while the runtime context is nil (as here).
	a.emitSysProxy("test", "nothing should be emitted")

	// friendlyStartErr's three branches.
	if got := friendlyStartErr(supervisor.ErrAlreadyRunning); got.Error() != "이미 켜져 있어요." {
		t.Fatalf("already-running message = %q", got.Error())
	}
	if got := friendlyStartErr(supervisor.ErrInvalidMode); got.Error() != "알 수 없는 모드예요." {
		t.Fatalf("invalid-mode message = %q", got.Error())
	}
	sentinel := errors.New("some other failure")
	if got := friendlyStartErr(sentinel); got != sentinel {
		t.Fatalf("default branch must pass the error through, got %v", got)
	}
}

// TestSetConfigValidation covers SetConfig's accept, reject-on-invalid, and
// reject-while-running paths.
func TestSetConfigValidation(t *testing.T) {
	a := NewApp("v1")

	// Valid config is accepted and echoed back.
	u := a.GetConfig()
	u.SetSystemProxy = false
	res, err := a.SetConfig(u)
	if err != nil {
		t.Fatalf("SetConfig(valid): %v", err)
	}
	if res.Config.DoHURL == "" {
		t.Fatal("SetConfig must echo the applied config")
	}

	// An invalid frag strategy is rejected with a friendly error.
	bad := a.GetConfig()
	bad.Frag = "nonsense"
	if _, err := a.SetConfig(bad); err == nil {
		t.Fatal("SetConfig must reject an unknown frag strategy")
	}

	// While the supervisor is running, SetConfig is rejected with the running
	// message. Start via the supervisor directly (ephemeral proxy ports, no
	// system-proxy) so nothing touches the OS.
	run := a.GetConfig()
	run.SetSystemProxy = false
	run.ProxyListen = "127.0.0.1:0"
	run.SocksListen = "127.0.0.1:0"
	if _, err := a.SetConfig(run); err != nil {
		t.Fatalf("SetConfig(run): %v", err)
	}
	if err := a.sup.Start(supervisor.ModeProxy); err != nil {
		t.Fatalf("sup.Start: %v", err)
	}
	defer func() { _ = a.sup.Stop() }()
	if _, err := a.SetConfig(run); err == nil {
		t.Fatal("SetConfig must be rejected while running")
	}
}

// TestStartStopLifecycle drives Start/Stop through the App with the system-proxy
// automation disabled and ephemeral ports, so no OS state is ever touched.
func TestStartStopLifecycle(t *testing.T) {
	redirectConfigDir(t)
	a := NewApp("v1")

	u := a.GetConfig()
	u.SetSystemProxy = false // critical: Start must never apply the real OS proxy
	u.DNSListen = "127.0.0.1:0"
	u.ProxyListen = "127.0.0.1:0"
	u.SocksListen = "127.0.0.1:0"
	if _, err := a.SetConfig(u); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	// Unknown mode → friendly invalid-mode error, no servers.
	if _, err := a.Start("bogus"); err == nil {
		t.Fatal("Start with an unknown mode must error")
	}

	st, err := a.Start("proxy")
	if err != nil {
		t.Fatalf("Start(proxy): %v", err)
	}
	if !st.Running {
		t.Fatal("Start(proxy) must report Running")
	}

	// A second Start while running → already-running error.
	if _, err := a.Start("proxy"); err == nil {
		t.Fatal("second Start must error with already-running")
	}

	if _, err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stop again → not-running error branch.
	if _, err := a.Stop(); err == nil {
		t.Fatal("Stop while not running must error")
	}
}

// TestMaybeApplySystemProxyBranches exercises every early-return of
// maybeApplySystemProxy WITHOUT ever reaching sysproxy.Apply (which would change
// the real OS proxy). The Apply path is deliberately never covered.
func TestMaybeApplySystemProxyBranches(t *testing.T) {
	redirectConfigDir(t) // defensive; none of these reach Apply/Restore

	// (a) SetSystemProxy off → returns before anything else, even with a valid
	// HTTP listener present.
	cfgOff := config.Default()
	cfgOff.SetSystemProxy = false
	off := &App{sup: supervisor.New(cfgOff)}
	off.maybeApplySystemProxy(supervisor.State{
		Listeners: []supervisor.Listener{{Kind: supervisor.KindHTTP, Up: true, Addr: "127.0.0.1:8080"}},
	})
	if off.sysproxyOn {
		t.Fatal("must not apply when SetSystemProxy is off")
	}

	// (b) SetSystemProxy on but no HTTP listener (resolve-mode shape) → httpAddr
	// stays empty → returns.
	noHTTP := &App{sup: supervisor.New(config.Default())}
	noHTTP.maybeApplySystemProxy(supervisor.State{
		Listeners: []supervisor.Listener{{Kind: supervisor.KindDNS, Up: true, Addr: "127.0.0.1:5353"}},
	})
	if noHTTP.sysproxyOn {
		t.Fatal("must not apply without an HTTP listener")
	}

	// (c) SetSystemProxy on with a malformed HTTP address → FromAddr fails and we
	// return before the running check and Apply.
	badAddr := &App{sup: supervisor.New(config.Default())}
	badAddr.maybeApplySystemProxy(supervisor.State{
		Listeners: []supervisor.Listener{{Kind: supervisor.KindHTTP, Up: true, Addr: "no-colon-here"}},
	})
	if badAddr.sysproxyOn {
		t.Fatal("FromAddr error must return before Apply")
	}
}

// TestRestoreSystemProxy covers both branches of restoreSystemProxy /
// restoreSystemProxyLocked. With redirected config dir and no backup on disk,
// sysproxy.Restore is a verified no-op.
func TestRestoreSystemProxy(t *testing.T) {
	redirectConfigDir(t)

	// Nothing owed → early return, flag stays false.
	none := &App{}
	none.restoreSystemProxy()
	if none.sysproxyOn {
		t.Fatal("restore must not set sysproxyOn")
	}

	// Owed → Restore finds no backup (nil), so the flag is cleared and a toast is
	// (silently, ctx nil) emitted.
	owed := &App{sysproxyOn: true}
	owed.restoreSystemProxy()
	if owed.sysproxyOn {
		t.Fatal("restore must clear sysproxyOn on success")
	}
}

// TestQuit covers Quit's happy path with a nil runtime context: it flags the
// quit, restores (no-op), stops the (idle) supervisor, and skips wruntime.Quit.
func TestQuit(t *testing.T) {
	redirectConfigDir(t)
	a := NewApp("t")
	a.Quit()
	if !a.quitting.Load() {
		t.Fatal("Quit must set the quitting flag")
	}
	if a.shouldPreventClose() {
		t.Fatal("after Quit, a window-close must be allowed through")
	}
}

// TestBeforeCloseAllowsQuit covers the branch where a real quit is in flight, so
// BeforeClose returns false and never touches the Wails runtime. The hide path
// is intentionally not exercised (it would call wruntime with a live context).
func TestBeforeCloseAllowsQuit(t *testing.T) {
	a := &App{}
	a.quitting.Store(true)
	if a.BeforeClose(context.Background()) {
		t.Fatal("with a quit in flight, BeforeClose must allow the close")
	}
}

// TestOnSecondInstanceNilCtx covers the early return when no runtime context is
// stored yet.
func TestOnSecondInstanceNilCtx(t *testing.T) {
	a := &App{}
	a.OnSecondInstance(options.SecondInstanceData{})
}

// TestShowWindowNilCtx covers showWindow's early return when the runtime context
// is nil (the wruntime reveal calls are unreachable in a unit test).
func TestShowWindowNilCtx(t *testing.T) {
	a := &App{}
	a.showWindow()
}

// TestStopTrayStub covers stopTray's external-loop path via a stubbed end hook,
// avoiding the systray.Quit branch (which would tear down a never-started tray).
func TestStopTrayStub(t *testing.T) {
	called := false
	a := &App{trayEnd: func() { called = true }}
	a.stopTray()
	if !called {
		t.Fatal("stopTray must invoke the external-loop end hook when set")
	}
}

// TestBackgroundCheck covers the quiet error return: a failing check returns
// without emitting. The "newer" path is deliberately not exercised here — it
// would require mutating the package global selfupdate.Version, which races (via
// -race) against the User-Agent read in a leftover background check goroutine
// from TestStartupStartTrayShutdown, with no synchronization edge between them.
func TestBackgroundCheck(t *testing.T) {
	errApp := &App{updater: stubErrChecker()}
	errApp.backgroundCheck(context.Background())
}

// TestCheckUpdateError covers CheckUpdate end-to-end via a failing transport.
func TestCheckUpdateError(t *testing.T) {
	a := &App{updater: stubErrChecker()}
	if _, err := a.CheckUpdate(); err == nil {
		t.Fatal("CheckUpdate must surface a failing check")
	}
}

// TestApplyUpdateError covers ApplyUpdate's failure return (and the StageStart
// progress callback, which fires before the network fetch) without driving the
// success path that would spawn a restart.
func TestApplyUpdateError(t *testing.T) {
	a := &App{updater: stubErrChecker()}
	if err := a.ApplyUpdate(); err == nil {
		t.Fatal("ApplyUpdate must return the underlying error")
	}
}

// TestRestart covers restart's success path. os.Executable() is the test binary;
// relaunch.Args prepends the internal relaunch flag, which the child test binary
// rejects as unknown and exits instantly — no recursion. restart then sleeps and
// calls Quit (safe with a nil runtime context).
func TestRestart(t *testing.T) {
	redirectConfigDir(t)
	a := NewApp("t")
	a.restart()
	if !a.quitting.Load() {
		t.Fatal("restart must quit after a successful relaunch")
	}
}

// TestShutdownSafe covers Shutdown with a stubbed tray end hook and redirected
// config dir, so it tears down cleanly without touching the OS proxy or calling
// systray.Quit on a never-started tray.
func TestShutdownSafe(t *testing.T) {
	redirectConfigDir(t)
	a := NewApp("t")
	a.updater = stubErrChecker()
	a.trayEnd = func() {} // rule: stub before Shutdown so stopTray takes the hook path
	a.Shutdown(context.Background())
	if a.runtimeContext() != nil {
		t.Fatal("Shutdown must clear the runtime context")
	}
}

// TestStartupStartTrayShutdown covers Startup (including startTray/startTrayItem)
// and a subsequent Shutdown. On darwin startTray registers via
// RunWithExternalLoop (a no-op under an external loop) and dispatches the status
// item's creation onto the GCD main queue, which never pumps under `go test`, so
// nothing native runs. The updater is stubbed (no network) and the config dir is
// redirected (RecoverStale is a no-op). The tray end hook is replaced before
// Shutdown so the real external-loop teardown never runs.
func TestStartupStartTrayShutdown(t *testing.T) {
	redirectConfigDir(t)
	a := NewApp("t")
	a.updater = stubErrChecker()
	a.Startup(context.Background())
	time.Sleep(100 * time.Millisecond) // let the one-shot background check finish quietly
	a.trayEnd = func() {}              // never run the real external-loop end hook
	a.Shutdown(context.Background())
}
