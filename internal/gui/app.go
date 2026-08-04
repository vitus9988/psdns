// Package gui is the Wails-bound control surface for the psdns desktop app. The
// App struct's exported methods are callable from the frontend as
// window.go.gui.App.*. All real work lives in internal/supervisor (start/stop)
// and internal/selfupdate (update); this layer only marshals to/from the UI.
package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
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

// Test seams: production code calls these package vars, which unit tests swap
// for fakes — the real implementations either mutate the OS web proxy
// (sysproxy.*) or log.Fatal without a live Wails runtime context (wruntime.*),
// so neither can run under `go test`. Defaults are the real implementations.
var (
	sysproxySupported     = sysproxy.Supported
	sysproxyApply         = sysproxy.Apply
	sysproxyRestore       = sysproxy.Restore
	sysproxyDisable       = sysproxy.DisableApplied
	sysproxyRecoverStale  = sysproxy.RecoverStale
	sysproxyCurrent       = sysproxy.Current
	sysproxyAlive         = sysproxy.Alive
	wailsEventsEmit       = wruntime.EventsEmit
	wailsWindowHide       = wruntime.WindowHide
	wailsWindowShow       = wruntime.WindowShow
	wailsWindowUnminimise = wruntime.WindowUnminimise
	wailsShow             = wruntime.Show
	wailsQuit             = wruntime.Quit
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
	// startupNotice holds a message raised before the frontend could listen for
	// events; the UI collects it via StartupNotice during init. Guarded by ctxMu.
	startupNotice string

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

	// instanceLock is the held single-instance lock file (see instancelock.go);
	// nil when the guard was skipped. secondInstance records that this process
	// lost the guard, so Start refuses even if the exit is stubbed out.
	instanceLock   *os.File
	secondInstance atomic.Bool
	// signalOnce keeps the SIGINT/SIGTERM handler to one installation, since
	// Startup is the only caller but tests exercise it directly too.
	signalOnce sync.Once
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
	// Before anything else, and specifically before RecoverStale: a duplicate
	// launch that got past Wails's own SingleInstanceLock (see instancelock.go)
	// must not clean up "stale" state that a live instance still owns, and must
	// not go on to fight it for :53.
	if !a.acquireInstanceLock() {
		a.secondInstance.Store(true)
		a.refuseSecondInstance()
		return
	}
	a.installSignalHandler()
	a.startTray()
	// A previous run may have crashed or been force-killed with the OS proxy
	// still pointed at us; clean that up first so a fresh Start snapshots the
	// real prior state. No-op on a clean start (no backup left behind). Under
	// sysproxyMu so it cannot interleave with an early Start's apply on the
	// shared backup file.
	a.sysproxyMu.Lock()
	_, err := sysproxyRecoverStale()
	a.sysproxyMu.Unlock()
	if err != nil {
		log.Printf("gui: stale system-proxy cleanup failed: %v", err)
		// A failure here means a previous run's OS proxy may still be in place —
		// the user's network may be routed through a proxy that no longer exists.
		// Park it for the frontend to pick up rather than emitting: at Startup the
		// frontend has not yet subscribed, so an event would be lost.
		a.setStartupNotice("이전 실행이 남긴 시스템 프록시 설정을 정리하지 못했어요. " +
			"인터넷이 안 되면 OS 네트워크 설정에서 프록시를 꺼 주세요.")
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
	wailsWindowHide(ctx)
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
	a.releaseInstanceLock()
	a.setRuntimeContext(nil)
}

// OnSecondInstance brings the existing window to the front when the user
// launches the app again (wired to SingleInstanceLock).
func (a *App) OnSecondInstance(_ options.SecondInstanceData) {
	if ctx := a.runtimeContext(); ctx != nil {
		a.revealWindow(ctx)
	}
}

// revealWindow brings the window back after a hide-to-tray or a minimise. All
// three calls are needed and must run together: WindowShow undoes the
// BeforeClose WindowHide, WindowUnminimise undoes a minimise, and Show raises
// and focuses it — a window hidden by WindowHide will not reappear on
// Unminimise+Show alone. Shared by the tray click (showWindow) and
// OnSecondInstance so neither path can drift from the full sequence.
func (a *App) revealWindow(ctx context.Context) {
	wailsWindowShow(ctx)
	wailsWindowUnminimise(ctx)
	wailsShow(ctx)
}

func (a *App) backgroundCheck(ctx context.Context) {
	res, err := a.updater.Check(ctx, false)
	if err != nil || !res.Newer {
		return // offline, rate-limited, cancelled, or already up to date: stay quiet
	}
	if ctx := a.runtimeContext(); ctx != nil {
		wailsEventsEmit(ctx, "update:available", res)
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
	if a.secondInstance.Load() {
		return a.sup.Status(), errors.New("이미 다른 psdns 가 실행 중이에요. 그 창에서 켜 주세요.")
	}
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
	if !sysproxySupported() {
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
	// Don't silently clobber another local filtering proxy (e.g. AdGuard): if the
	// OS already routes web traffic through a *different* loopback proxy that is
	// actually accepting connections, applying would route around its filtering
	// (its ad-blocking would stop working while psdns is on), so warn and skip.
	// No backup is written and sysproxyOn stays false, so Stop/RecoverStale
	// remain no-ops (no dead-proxy risk). A dead loopback entry is not worth
	// protecting — it is the leftover of a crashed run (often our own, after a
	// port fallback moved us off its port) and is taken over; Apply's capture
	// hygiene keeps that dead state out of the backup. A detection error is
	// fail-open — Apply's own capture would surface the same failure. The user
	// can turn off that app or the auto-set toggle and set psdns by hand.
	if cur, derr := sysproxyCurrent(); derr == nil && cur.ConflictsWith(s.Host, s.Port) &&
		sysproxyAlive(cur.Host, cur.Port, sysproxy.ProbeTimeout) {
		a.emitSysProxy("conflict", fmt.Sprintf(
			"다른 로컬 프록시(%s)가 시스템 프록시로 설정돼 있어 자동 설정을 건너뛰었어요. "+
				"그 앱(예: AdGuard)을 끄거나, '시스템 프록시 자동 설정'을 끄고 아래 주소를 직접 넣어 주세요.",
			net.JoinHostPort(cur.Host, strconv.Itoa(cur.Port))))
		return
	}
	// Mark the restore as owed before Apply touches the OS: Apply writes its
	// backup first, so a partial apply failure (e.g. macOS admin refusal mid-way)
	// must still be undone by this session's Stop/Shutdown, not left only for the
	// next launch's RecoverStale.
	a.sysproxyOn = true
	if err := sysproxyApply(s); err != nil {
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
	restored, err := sysproxyRestore()
	if err != nil {
		a.emitSysProxy("error", "시스템 프록시를 원래대로 되돌리지 못했어요. 네트워크 설정을 확인해 주세요.")
		return
	}
	if !restored {
		// We applied (sysproxyOn) but the snapshot is gone — deleted as corrupt,
		// removed by a cleanup tool, or consumed by a duplicate instance. Treating
		// that as success is what used to leave the OS pointed at a listener that
		// is about to close, with no backup left for the next launch to recover
		// from, i.e. an internet outage the user has to fix by hand. We cannot
		// reproduce their previous setting, but we can stop routing through us.
		if derr := sysproxyDisable(); derr != nil {
			a.emitSysProxy("error", "시스템 프록시 원복 정보가 사라져 자동 복원에 실패했어요. OS 설정에서 프록시를 직접 꺼 주세요.")
			return
		}
		a.sysproxyOn = false
		a.emitSysProxy("restored", "원복 정보가 사라져 시스템 프록시를 껐어요. 이전에 쓰던 프록시가 있었다면 다시 지정해 주세요.")
		return
	}
	a.sysproxyOn = false
	a.emitSysProxy("restored", "시스템 프록시를 원래대로 되돌렸어요")
}

// StartupNotice returns a message about something that went wrong before the UI
// existed (currently: stale system-proxy cleanup failed), and clears it so it is
// shown once. The frontend polls this during init instead of listening for an
// event, because Startup runs before the frontend subscribes and an event there
// would go nowhere.
func (a *App) StartupNotice() string {
	a.ctxMu.Lock()
	defer a.ctxMu.Unlock()
	n := a.startupNotice
	a.startupNotice = ""
	return n
}

func (a *App) setStartupNotice(msg string) {
	a.ctxMu.Lock()
	a.startupNotice = msg
	a.ctxMu.Unlock()
}

// SystemProxySupported reports whether OS web-proxy automation is available on
// this platform, so the frontend can hide or disable the auto-set toggle where
// it can never take effect (unsupported OS, or Linux without a graphical session).
func (a *App) SystemProxySupported() bool { return sysproxySupported() }

// emitSysProxy sends a sysproxy:* event (a toast) to the frontend. No-op when the
// runtime context is gone (e.g. mid-shutdown).
func (a *App) emitSysProxy(kind, msg string) {
	if ctx := a.runtimeContext(); ctx != nil {
		wailsEventsEmit(ctx, "sysproxy:"+kind, msg)
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
			wailsEventsEmit(ctx, "update:progress", map[string]any{
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
			wailsEventsEmit(ctx, "update:error",
				"업데이트는 적용됐지만 자동 재시작에 실패했어요. 앱을 직접 다시 실행해 주세요.")
		}
		return
	}
	time.Sleep(restartDelay)
	a.Quit()
}

// installSignalHandler arranges for a SIGINT/SIGTERM to restore the OS proxy and
// release the ports before the process dies. Wails installs no signal handler of
// its own, so without this a `kill`, a Ctrl-C on a terminal launch, or a Linux
// session logout killed the app with the OS web proxy still pointed at us —
// leaving the user's network settings changed until a later launch's
// RecoverStale. On Windows SIGTERM is never delivered to a GUI process; the
// delegate hook (macOS) and RecoverStale (everywhere) remain the backstops.
func (a *App) installSignalHandler() {
	a.signalOnce.Do(func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		go a.awaitTerminationSignal(ch)
	})
}

// awaitTerminationSignal is installSignalHandler's body, split out so a test can
// deliver a signal without one. It runs on its own goroutine and never touches
// Wails, so it cannot interfere with the native main loop; the OS state is
// already put back by the time the process exits.
func (a *App) awaitTerminationSignal(ch <-chan os.Signal) {
	sig, ok := <-ch
	if !ok {
		return
	}
	log.Printf("gui: %v received, restoring system settings before exit", sig)
	a.shutdownForTermination()
	exitFn(0)
}

// shutdownForTermination undoes everything this process owes the OS when it is
// about to die without Wails's OnShutdown running: a macOS ⌘Q / Dock quit /
// logout (see tray_darwin.go — systray replaces Wails's app delegate, so those
// paths bypass OnShutdown entirely) or a SIGINT/SIGTERM. It is deliberately
// Wails-free and idempotent: sysproxyOn makes the restore run exactly once, and
// a second Stop just reports ErrNotRunning. quitting is set so a close event
// racing us cannot be turned into a hide-to-tray.
func (a *App) shutdownForTermination() {
	a.quitting.Store(true)
	a.restoreSystemProxy()
	if a.sup != nil {
		_ = a.sup.Stop()
	}
	a.releaseInstanceLock()
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
		wailsQuit(ctx)
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
