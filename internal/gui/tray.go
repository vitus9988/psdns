// tray.go gives the GUI a cross-platform tray presence — a notification-area
// icon on Windows/Linux, a menu-bar item on macOS — so closing the window can
// hide to the tray (see App.BeforeClose) instead of quitting.
//
// Wails v2 has no native tray, so we use energye/systray. Driving it differs by
// OS (see startTray): macOS/Linux let Wails own the one native loop and attach
// systray through RunWithExternalLoop, while Windows must run systray's window
// and its message loop on a single pinned thread. This is the ONLY file that
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

// startTray builds the tray icon and menu and starts systray. Safe to call from
// Startup: it never blocks. See stopTray for the matching teardown.
func (a *App) startTray() {
	onReady := func() {
		applyTrayIcon()
		systray.SetTooltip("psdns")

		mShow := systray.AddMenuItem("열기", "psdns 창 열기")
		mQuit := systray.AddMenuItem("종료하기", "psdns 종료")
		mShow.Click(func() { a.showWindow() })
		mQuit.Click(func() { a.Quit() })

		// Left-click reopens the window. Registering any click handler also arms
		// systray's click dispatch (inert otherwise on macOS). Right-click is
		// left unset so it falls through to systray's default: showing this menu.
		systray.SetOnClick(func(systray.IMenu) { a.showWindow() })
	}

	if runtime.GOOS == "windows" {
		// Win32 delivers a window's messages only to the thread that created it.
		// RunWithExternalLoop makes the tray window on the caller goroutine but
		// pumps GetMessage on a separate, unlocked goroutine, so tray clicks land
		// in a queue nothing reads and never dispatch (the icon still shows —
		// that's Shell_NotifyIcon, which needs no loop). Run the window AND its
		// message loop on one pinned thread instead. systray.Run blocks, so it
		// gets its own goroutine; it coexists with Wails's main-thread loop
		// because Win32 allows a message loop per thread.
		go func() {
			runtime.LockOSThread()
			systray.Run(onReady, func() {})
		}()
		return
	}

	// macOS/Linux: Wails owns the single native loop. systray.Run would call
	// [NSApp run] a second time on macOS and conflict; the external-loop hooks
	// install the status item without it.
	start, end := systray.RunWithExternalLoop(onReady, func() {})
	a.trayEnd = end
	start()
}

// stopTray tears the tray down on Shutdown, matching whichever path startTray
// took: the external-loop end hook on macOS/Linux, or systray.Quit on Windows
// (where startTray used systray.Run and left trayEnd nil).
func (a *App) stopTray() {
	if a.trayEnd != nil {
		a.trayEnd()
		return
	}
	systray.Quit()
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
