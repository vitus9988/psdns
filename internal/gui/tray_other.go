//go:build !darwin

package gui

// startTrayItem runs systray's start() inline. Only Linux reaches it (Windows
// takes the systray.Run path in startTray), and Linux's tray backend has no
// main-thread NSWindow constraint, so no main-queue hop is needed.
func startTrayItem(start func()) { start() }

// setTerminateHandler is a no-op off macOS: only there does starting the tray
// replace the app delegate and take the OS-driven quit path away from Wails (see
// tray_darwin.go). Elsewhere Wails's OnShutdown still runs, and the signal
// handler in app.go covers SIGTERM/SIGINT.
func setTerminateHandler(func()) {}
