// tray.go gives the GUI a cross-platform tray presence — a notification-area
// icon on Windows/Linux, a menu-bar item on macOS — so closing the window can
// hide to the tray (see App.BeforeClose) instead of quitting.
//
// Wails v2 has no native tray, so we use energye/systray. Because Wails owns the
// main loop, we drive it through RunWithExternalLoop, whose start()/end() hooks
// are non-blocking and fire from the App lifecycle. This is the ONLY file that
// imports systray, keeping its cgo dependency inside the GUI (the CGO-free CLI
// never compiles it).
package gui

import (
	_ "embed"
	"runtime"

	"github.com/energye/systray"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed icons/tray.png
var trayPNG []byte

//go:embed icons/tray.ico
var trayICO []byte

// startTray builds the tray icon and menu, then registers systray's start hook
// with the already-running Wails event loop. Safe to call from Startup: start()
// returns immediately and onReady runs on systray's own goroutine.
func (a *App) startTray() {
	onReady := func() {
		applyTrayIcon()
		systray.SetTooltip("psdns")

		mShow := systray.AddMenuItem("열기", "psdns 창 열기")
		mQuit := systray.AddMenuItem("종료하기", "psdns 종료")
		mShow.Click(func() { a.showWindow() })
		mQuit.Click(func() { a.Quit() })

		// A left-click reopens the window. Registering any click handler also
		// arms systray's click dispatch on macOS — without one the menu-bar
		// item is inert. Right-click is left unset so it falls through to
		// systray's default: showing this menu.
		systray.SetOnClick(func(systray.IMenu) { a.showWindow() })
	}

	start, end := systray.RunWithExternalLoop(onReady, func() {})
	a.trayEnd = end
	start()
}

// applyTrayIcon feeds systray the icon format it expects per OS: .ico on
// Windows, .png elsewhere.
func applyTrayIcon() {
	if runtime.GOOS == "windows" {
		systray.SetIcon(trayICO)
		return
	}
	systray.SetIcon(trayPNG)
}

// showWindow reveals the window after a hide-to-tray (or a minimise), reusing
// the same reveal sequence as OnSecondInstance.
func (a *App) showWindow() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowShow(a.ctx)
	wruntime.WindowUnminimise(a.ctx)
	wruntime.Show(a.ctx)
}
