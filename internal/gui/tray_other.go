//go:build !darwin

package gui

// startTrayItem runs systray's start() inline. Only Linux reaches it (Windows
// takes the systray.Run path in startTray), and Linux's tray backend has no
// main-thread NSWindow constraint, so no main-queue hop is needed.
func startTrayItem(start func()) { start() }
