// Package gui is the Wails-bound control surface for the psdns desktop app. The
// App struct's exported methods are callable from the frontend as
// window.go.gui.App.*. All real work lives in internal/supervisor (start/stop)
// and internal/selfupdate (update); this layer only marshals to/from the UI.
package gui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/selfupdate"
	"github.com/vitus9988/psdns/internal/supervisor"
	"github.com/vitus9988/psdns/internal/uiconfig"
	"github.com/wailsapp/wails/v2/pkg/options"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// settleDelay is how long Start waits before reading status, so an immediate
// bind failure (e.g. :53 without privilege) is reflected in the response.
const settleDelay = 150 * time.Millisecond

// App is the object bound to the Wails frontend.
type App struct {
	ctx     context.Context
	version string
	sup     *supervisor.Supervisor
	updater *selfupdate.Checker

	// quitting distinguishes a real quit (tray "종료하기" / the in-app button,
	// which route through Quit) from the window's close button: BeforeClose
	// hides the window unless a quit is already in flight.
	quitting atomic.Bool
	// trayEnd tears the tray icon down on macOS/Linux (the external-loop end
	// hook); set by startTray, consumed by stopTray. Nil on Windows, which runs
	// systray.Run and tears down via systray.Quit instead.
	trayEnd func()
}

// NewApp builds the App. version is the GUI build version shown in the UI.
func NewApp(version string) *App {
	return &App{
		version: version,
		sup:     supervisor.New(config.Default()),
		updater: selfupdate.NewChecker(&http.Client{Timeout: 15 * time.Second}),
	}
}

// Startup is wired to options.App.OnStartup: it captures the Wails runtime
// context and starts a one-shot background update check.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	a.startTray()
	go a.backgroundCheck()
}

// BeforeClose is wired to OnBeforeClose. Returning true cancels the close, so
// the window's X button hides to the tray and the servers keep running. A real
// quit (via Quit) sets quitting first, so this lets that one through.
func (a *App) BeforeClose(ctx context.Context) (prevent bool) {
	if a.quitting.Load() {
		return false
	}
	wruntime.WindowHide(ctx)
	return true
}

// Shutdown is wired to OnShutdown: tear down the tray and any running servers.
func (a *App) Shutdown(ctx context.Context) {
	a.stopTray()
	if a.sup != nil {
		_ = a.sup.Stop()
	}
}

// OnSecondInstance brings the existing window to the front when the user
// launches the app again (wired to SingleInstanceLock).
func (a *App) OnSecondInstance(_ options.SecondInstanceData) {
	if a.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(a.ctx)
	wruntime.Show(a.ctx)
}

func (a *App) backgroundCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := a.updater.Check(ctx, false)
	if err != nil || !res.Newer {
		return // offline, rate-limited, or already up to date: stay quiet
	}
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "update:available", res)
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
	return a.sup.WaitSettled(settleDelay), nil
}

// Stop tears down all servers.
func (a *App) Stop() (supervisor.State, error) {
	if err := a.sup.Stop(); err != nil {
		return a.sup.Status(), err
	}
	return a.sup.Status(), nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.updater.Check(ctx, true)
}

// ApplyUpdate downloads, verifies, and installs the newest release, emitting
// "update:progress" events, then restarts the app.
func (a *App) ApplyUpdate() error {
	err := a.updater.Apply(context.Background(), func(stage selfupdate.Stage, pct float64) {
		if a.ctx != nil {
			wruntime.EventsEmit(a.ctx, "update:progress", map[string]any{
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

// restart launches a fresh copy of the (now-updated) executable and quits.
func (a *App) restart() {
	if exe, err := os.Executable(); err == nil {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		_ = cmd.Start()
	}
	time.Sleep(400 * time.Millisecond)
	a.Quit()
}

// Quit stops servers and closes the app. Setting quitting first tells
// BeforeClose to allow the close instead of hiding to the tray.
func (a *App) Quit() {
	a.quitting.Store(true)
	if a.sup != nil {
		_ = a.sup.Stop()
	}
	if a.ctx != nil {
		wruntime.Quit(a.ctx)
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
