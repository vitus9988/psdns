// Package gui is the Wails-bound control surface for the psdns desktop app. The
// App struct's exported methods are callable from the frontend as
// window.go.gui.App.*. All real work lives in internal/supervisor (start/stop)
// and internal/selfupdate (update); this layer only marshals to/from the UI.
package gui

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/relaunch"
	"github.com/vitus9988/psdns/internal/selfupdate"
	"github.com/vitus9988/psdns/internal/supervisor"
	"github.com/vitus9988/psdns/internal/sysproxy"
	"github.com/vitus9988/psdns/internal/uiconfig"
	"github.com/wailsapp/wails/v2/pkg/options"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	// settleDelay is how long Start waits before reading status, so an immediate
	// bind failure (e.g. :53 without privilege) is reflected in the response.
	settleDelay = 150 * time.Millisecond
	// restartDelay gives the freshly launched copy a moment to come up before
	// this process quits after an update.
	restartDelay = 400 * time.Millisecond
	// updateCheckTimeout bounds both the silent startup update check and the
	// manual "업데이트 확인" check.
	updateCheckTimeout = 20 * time.Second
	// applyTimeout bounds a full ApplyUpdate (metadata + checksums + archive
	// download + extract + replace). It is generous because the archive is a few
	// MB and the user may be on a slow link — the old bound was the shared HTTP
	// client's 15s, which could abort a legitimate multi-MB download mid-stream.
	applyTimeout = 5 * time.Minute
)

// App is the object bound to the Wails frontend.
type App struct {
	version string
	sup     *supervisor.Supervisor
	updater *selfupdate.Checker

	ctxMu sync.Mutex
	ctx   context.Context
	// bgCancel cancels the in-flight startup update check so Shutdown can stop
	// it rather than let it run (and emit) against a torn-down context.
	bgCancel context.CancelFunc

	// quitting distinguishes a real quit (tray "종료하기" / the in-app button,
	// which route through Quit) from the window's close button: BeforeClose
	// hides the window unless a quit is already in flight.
	quitting atomic.Bool
	// trayEnd tears the tray icon down on macOS/Linux (the external-loop end
	// hook); set by startTray, consumed by stopTray. Nil on Windows, which runs
	// systray.Run and tears down via systray.Quit instead.
	trayEnd func()
	// sysproxyMu serializes the apply/restore sequence and guards sysproxyOn, so a
	// quick Start/Stop pair (Wails may dispatch bound calls on separate goroutines)
	// cannot interleave the backup capture/write/restore/delete on the shared file.
	sysproxyMu sync.Mutex
	// sysproxyOn is true while this app has the OS web proxy pointed at us, so a
	// restore is a no-op unless we changed it. It is set before Apply touches the
	// OS, so even a partial apply failure is still undone by this session's Stop.
	sysproxyOn bool
}

// NewApp builds the App. version is the GUI build version shown in the UI.
func NewApp(version string) *App {
	return &App{
		version: version,
		sup:     supervisor.New(config.Default()),
		// No fixed Client.Timeout: it would cap the whole request including the
		// archive body read, which can legitimately exceed it on a slow link.
		// Each call sets its own context deadline instead (updateCheckTimeout for
		// checks, applyTimeout for ApplyUpdate); the transport timeouts below still
		// catch a dead connection or a server that never sends headers.
		updater: selfupdate.NewChecker(&http.Client{
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
			},
		}),
	}
}

// Startup is wired to options.App.OnStartup: it captures the Wails runtime
// context and starts a one-shot background update check.
func (a *App) Startup(ctx context.Context) {
	a.setRuntimeContext(ctx)
	a.startTray()
	// A previous run may have crashed or been force-killed with the OS proxy
	// still pointed at us; clean that up first so a fresh Start snapshots the
	// real prior state. No-op on a clean start (no backup left behind).
	if _, err := sysproxy.RecoverStale(); err != nil {
		log.Printf("gui: stale system-proxy cleanup failed: %v", err)
	}
	bgCtx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	a.setBackgroundCancel(cancel)
	go a.backgroundCheck(bgCtx)
}

// BeforeClose is wired to OnBeforeClose. Returning true cancels the close, so
// the window's X button hides to the tray and the servers keep running. A real
// quit (via Quit) sets quitting first, so this lets that one through.
func (a *App) BeforeClose(ctx context.Context) (prevent bool) {
	if !a.shouldPreventClose() {
		return false
	}
	wruntime.WindowHide(ctx)
	return true
}

// shouldPreventClose reports whether a window-close should be intercepted
// (hidden to the tray) rather than allowed to quit. It is the Wails-free core
// of BeforeClose, kept separate so the decision can be unit-tested. A real quit
// routes through Quit, which sets quitting first.
func (a *App) shouldPreventClose() bool {
	return !a.quitting.Load()
}

// Shutdown is wired to OnShutdown: stop the in-flight update check, tear down
// the tray and any running servers, and drop the runtime context so a late
// background goroutine cannot emit against it.
func (a *App) Shutdown(ctx context.Context) {
	a.cancelBackgroundCheck()
	// Restore the OS proxy on every real exit (tray quit, window close, normal
	// OnShutdown). A force-kill skips this; the next Startup's RecoverStale
	// covers that — the two are a pair.
	a.restoreSystemProxy()
	a.stopTray()
	if a.sup != nil {
		if err := a.sup.Stop(); err != nil {
			log.Printf("gui: server shutdown: %v", err)
		}
	}
	a.setRuntimeContext(nil)
}

// OnSecondInstance brings the existing window to the front when the user
// launches the app again (wired to SingleInstanceLock).
func (a *App) OnSecondInstance(_ options.SecondInstanceData) {
	ctx := a.runtimeContext()
	if ctx == nil {
		return
	}
	wruntime.WindowUnminimise(ctx)
	wruntime.Show(ctx)
}

func (a *App) backgroundCheck(ctx context.Context) {
	res, err := a.updater.Check(ctx, false)
	if err != nil || !res.Newer {
		return // offline, rate-limited, cancelled, or already up to date: stay quiet
	}
	if ctx := a.runtimeContext(); ctx != nil {
		wruntime.EventsEmit(ctx, "update:available", res)
	}
}

// Version returns the GUI build version.
func (a *App) Version() string { return a.version }

// GetStatus returns the current runtime status.
func (a *App) GetStatus() supervisor.State { return a.sup.Status() }

// Start brings up the servers for mode ("proxy"|"resolve"|"run"). On a caller
// error (already running, invalid mode, bad config) the promise rejects with a
// friendly message; bind failures instead arrive in the resolved State's
// Listeners[].Err.
func (a *App) Start(mode string) (supervisor.State, error) {
	if err := a.sup.Start(supervisor.Mode(mode)); err != nil {
		return a.sup.Status(), friendlyStartErr(err)
	}
	st := a.sup.WaitSettled(settleDelay)
	a.maybeApplySystemProxy(st)
	return st, nil
}

// Stop tears down all servers. The system proxy is restored before the servers
// go down so the OS is never left pointing at a closing listener. The restore
// and the server teardown happen under sysproxyMu so they cannot interleave with
// a maybeApplySystemProxy from a racing Start (Wails may dispatch Start and Stop
// on separate goroutines): either the apply runs first and this restore undoes
// it, or this teardown runs first and the apply sees Running==false and skips.
func (a *App) Stop() (supervisor.State, error) {
	a.sysproxyMu.Lock()
	a.restoreSystemProxyLocked()
	err := a.sup.Stop()
	a.sysproxyMu.Unlock()
	if err != nil {
		return a.sup.Status(), err
	}
	return a.sup.Status(), nil
}

// maybeApplySystemProxy points the OS web proxy at the live HTTP proxy when the
// user has the option enabled and an HTTP listener actually came up. Resolve mode
// has no HTTP listener, so it is naturally skipped. Failure is non-fatal —
// protection still works for apps pointed at the proxy by hand — so it only
// surfaces a toast.
func (a *App) maybeApplySystemProxy(st supervisor.State) {
	if !a.sup.Config().SetSystemProxy {
		return
	}
	if !sysproxy.Supported() {
		return // no OS automation on this platform; the UI hides the toggle, so stay silent
	}
	var httpAddr string
	for _, l := range st.Listeners {
		if l.Kind == supervisor.KindHTTP && l.Up {
			httpAddr = l.Addr // actual bound address, port fallback included
			break
		}
	}
	if httpAddr == "" {
		return // resolve mode, or the HTTP listener failed to bind
	}
	s, err := sysproxy.FromAddr(httpAddr, sysproxy.DefaultBypass())
	if err != nil {
		return
	}
	a.sysproxyMu.Lock()
	defer a.sysproxyMu.Unlock()
	// Guard the Start/Stop race: Wails may dispatch a Stop on another goroutine
	// during Start's settle delay. Stop takes sysproxyMu across its server
	// teardown, so if it already ran, the servers are down by now — applying would
	// point the OS proxy at a dead listener that this session would never restore
	// (Stop's restore already ran and found nothing owed). Re-check under the lock.
	if !a.sup.Status().Running {
		return
	}
	// Mark the restore as owed before Apply touches the OS: Apply writes its
	// backup first, so a partial apply failure (e.g. macOS admin refusal mid-way)
	// must still be undone by this session's Stop/Shutdown, not left only for the
	// next launch's RecoverStale.
	a.sysproxyOn = true
	if err := sysproxy.Apply(s); err != nil {
		a.emitSysProxy("error", "시스템 프록시 자동 설정에 실패했어요. 아래 주소를 복사해 브라우저에 직접 넣어 주세요.")
		return
	}
	a.emitSysProxy("applied", "시스템 프록시를 자동으로 맞췄어요")
}

// restoreSystemProxy puts the OS proxy back if we changed it. The mutex + flag
// make the Stop→Shutdown double call restore exactly once; on failure the flag
// stays set so a later call retries (and the on-disk backup lets the next launch
// recover as a final backstop).
func (a *App) restoreSystemProxy() {
	a.sysproxyMu.Lock()
	defer a.sysproxyMu.Unlock()
	a.restoreSystemProxyLocked()
}

// restoreSystemProxyLocked is the body of restoreSystemProxy; the caller must
// already hold sysproxyMu. Stop calls this directly so the restore and the
// server teardown stay in one critical section (see Stop).
func (a *App) restoreSystemProxyLocked() {
	if !a.sysproxyOn {
		return
	}
	if err := sysproxy.Restore(); err != nil {
		a.emitSysProxy("error", "시스템 프록시를 원래대로 되돌리지 못했어요. 네트워크 설정을 확인해 주세요.")
		return
	}
	a.sysproxyOn = false
	a.emitSysProxy("restored", "시스템 프록시를 원래대로 되돌렸어요")
}

// SystemProxySupported reports whether OS web-proxy automation is available on
// this platform, so the frontend can hide or disable the auto-set toggle where
// it can never take effect (unsupported OS, or Linux without a graphical session).
func (a *App) SystemProxySupported() bool { return sysproxy.Supported() }

// emitSysProxy sends a sysproxy:* event (a toast) to the frontend. No-op when the
// runtime context is gone (e.g. mid-shutdown).
func (a *App) emitSysProxy(kind, msg string) {
	if ctx := a.runtimeContext(); ctx != nil {
		wruntime.EventsEmit(ctx, "sysproxy:"+kind, msg)
	}
}

// GetConfig returns the current configuration for the UI.
func (a *App) GetConfig() uiconfig.Config { return uiconfig.FromConfig(a.sup.Config()) }

// SetConfig validates and stores config (rejected while running).
func (a *App) SetConfig(u uiconfig.Config) (uiconfig.SetResult, error) {
	c, warns, err := u.ToConfig()
	if err != nil {
		return uiconfig.SetResult{}, err
	}
	if err := a.sup.SetConfig(c); err != nil {
		if errors.Is(err, supervisor.ErrAlreadyRunning) {
			return uiconfig.SetResult{}, errors.New("실행 중에는 설정을 바꿀 수 없어요. 먼저 중지한 뒤 바꿔 주세요.")
		}
		return uiconfig.SetResult{}, err
	}
	return uiconfig.SetResult{Config: uiconfig.FromConfig(a.sup.Config()), Warnings: warns}, nil
}

// CheckUpdate forces a fresh update check (the manual "업데이트 확인" button).
func (a *App) CheckUpdate() (selfupdate.CheckResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	return a.updater.Check(ctx, true)
}

// ApplyUpdate downloads, verifies, and installs the newest release, emitting
// "update:progress" events, then restarts the app.
func (a *App) ApplyUpdate() error {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()
	err := a.updater.Apply(ctx, func(stage selfupdate.Stage, pct float64) {
		if ctx := a.runtimeContext(); ctx != nil {
			wruntime.EventsEmit(ctx, "update:progress", map[string]any{
				"stage": string(stage), "pct": pct,
			})
		}
	})
	if err != nil {
		return err
	}
	go a.restart()
	return nil
}

// restart launches a fresh copy of the (now-updated) executable and quits. If
// the relaunch cannot be started, it keeps this process alive and tells the UI
// instead of quitting into nothing — the replaced binary is already on disk, so
// the user can run it manually.
func (a *App) restart() {
	exe, err := os.Executable()
	if err == nil {
		cmd := exec.Command(exe, relaunch.Args(os.Getpid(), os.Args[1:])...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		err = cmd.Start()
	}
	if err != nil {
		if ctx := a.runtimeContext(); ctx != nil {
			wruntime.EventsEmit(ctx, "update:error",
				"업데이트는 적용됐지만 자동 재시작에 실패했어요. 앱을 직접 다시 실행해 주세요.")
		}
		return
	}
	time.Sleep(restartDelay)
	a.Quit()
}

// Quit stops servers and closes the app. Setting quitting first tells
// BeforeClose to allow the close instead of hiding to the tray.
func (a *App) Quit() {
	a.quitting.Store(true)
	a.restoreSystemProxy()
	if a.sup != nil {
		_ = a.sup.Stop()
	}
	if ctx := a.runtimeContext(); ctx != nil {
		wruntime.Quit(ctx)
	}
}

func (a *App) setRuntimeContext(ctx context.Context) {
	a.ctxMu.Lock()
	a.ctx = ctx
	a.ctxMu.Unlock()
}

func (a *App) runtimeContext() context.Context {
	a.ctxMu.Lock()
	defer a.ctxMu.Unlock()
	return a.ctx
}

func (a *App) setBackgroundCancel(cancel context.CancelFunc) {
	a.ctxMu.Lock()
	a.bgCancel = cancel
	a.ctxMu.Unlock()
}

func (a *App) cancelBackgroundCheck() {
	a.ctxMu.Lock()
	cancel := a.bgCancel
	a.bgCancel = nil
	a.ctxMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func friendlyStartErr(err error) error {
	switch {
	case errors.Is(err, supervisor.ErrAlreadyRunning):
		return errors.New("이미 켜져 있어요.")
	case errors.Is(err, supervisor.ErrInvalidMode):
		return errors.New("알 수 없는 모드예요.")
	default:
		return err
	}
}
