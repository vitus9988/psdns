//go:build darwin

package gui

// On macOS the systray status item is an NSWindow, which AppKit requires to be
// created on the main thread. Wails runs OnStartup (where startTray is called)
// on a non-main goroutine, so startTrayItem dispatches systray's start() onto the
// main queue via GCD instead of running it inline. By OnStartup time Wails is
// already in [NSApp run], so the main queue is live and the block runs. This cgo
// lives only in this darwin file so the CGO-free CLI and other OSes never see it.

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation
#include <dispatch/dispatch.h>
extern void psdnsRunTrayStart(void);
static inline void psdnsDispatchTrayStart(void) {
    dispatch_async(dispatch_get_main_queue(), ^{ psdnsRunTrayStart(); });
}
*/
import "C"

// trayStart holds systray's start() so the exported C callback can invoke it on
// the Cocoa main thread. Set once from startTrayItem during App.Startup, then
// read once on the main queue — no concurrent access.
var trayStart func()

//export psdnsRunTrayStart
func psdnsRunTrayStart() {
	if trayStart != nil {
		trayStart()
	}
}

// startTrayItem runs systray's start() — which creates the NSStatusItem (an
// NSWindow) — on the main thread by hopping onto the main GCD queue.
func startTrayItem(start func()) {
	trayStart = start
	C.psdnsDispatchTrayStart()
}
