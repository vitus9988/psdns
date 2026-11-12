//go:build darwin

package gui

// On macOS the systray status item is an NSWindow, which AppKit requires to be
// created on the main thread. Wails runs OnStartup (where startTray is called)
// on a non-main goroutine, so startTrayItem dispatches systray's start() onto the
// main queue via GCD instead of running it inline. By OnStartup time Wails is
// already in [NSApp run], so the main queue is live and the block runs. This cgo
// lives only in this darwin file so the CGO-free CLI and other OSes never see it.
//
// The same main-queue block then repairs a side effect of starting systray:
// systray's nativeStart does [NSApp setDelegate:] with its own delegate, which
// replaces Wails's. Wails's delegate is what turned ⌘Q, the Dock's Quit and
// logout into OnBeforeClose/OnShutdown (via applicationShouldTerminate:), and
// systray's implements only applicationWillTerminate: — so those quits used to
// kill the process with the OS web proxy still pointed at us, leaving the user's
// network settings changed until a later launch's RecoverStale cleaned up.
//
// The repair adds applicationShouldTerminate: to the live delegate's class
// rather than putting a different delegate back, because systray's left-click
// handler is a nil-target action that is only delivered if its own delegate is
// still NSApp's delegate — swapping the object back would break tray clicks.
// The added method restores the OS proxy and stops the servers, then returns
// NSTerminateNow, which is exactly what AppKit does today in the absence of the
// method: same quit, minus the leftover proxy. Adding (never replacing) also
// means a future systray that implements the selector itself keeps its own
// implementation — class_addMethod simply fails and we leave it alone.

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include <dispatch/dispatch.h>
#include <objc/runtime.h>
#include <stdio.h>
#import <Cocoa/Cocoa.h>

extern void psdnsRunTrayStart(void);
extern void psdnsHandleTerminate(void);

// psdnsShouldTerminate is the injected applicationShouldTerminate: body. The
// teardown runs synchronously (a networksetup/registry round trip on the main
// thread, in a process that is exiting anyway) so it cannot be cut short by the
// termination that follows; returning NSTerminateNow keeps the quit immediate
// and never cancels a logout or restart.
static NSUInteger psdnsShouldTerminate(id self, SEL _cmd, id sender) {
    psdnsHandleTerminate();
    return NSTerminateNow;
}

static void psdnsInstallTerminateHandler(void) {
    id delegate = [NSApp delegate];
    if (delegate == nil) {
        return;
    }
    char types[16];
    snprintf(types, sizeof(types), "%s@:@", @encode(NSUInteger));
    class_addMethod(object_getClass(delegate), @selector(applicationShouldTerminate:),
                    (IMP)(void *)psdnsShouldTerminate, types);
}

static inline void psdnsDispatchTrayStart(void) {
    dispatch_async(dispatch_get_main_queue(), ^{ psdnsRunTrayStart(); });
}
*/
import "C"

// trayStart holds systray's start() so the exported C callback can invoke it on
// the Cocoa main thread. Set once from startTrayItem during App.Startup, then
// read once on the main queue — no concurrent access.
var trayStart func()

// terminateHandler is the App teardown the injected applicationShouldTerminate:
// runs. Set alongside trayStart, so it is in place before the delegate can
// receive a quit.
var terminateHandler func()

//export psdnsRunTrayStart
func psdnsRunTrayStart() {
	if trayStart != nil {
		trayStart()
	}
	// Only now does systray's delegate exist: install on top of it.
	C.psdnsInstallTerminateHandler()
}

//export psdnsHandleTerminate
func psdnsHandleTerminate() {
	if terminateHandler != nil {
		terminateHandler()
	}
}

// startTrayItem runs systray's start() — which creates the NSStatusItem (an
// NSWindow) — on the main thread by hopping onto the main GCD queue.
func startTrayItem(start func()) {
	trayStart = start
	C.psdnsDispatchTrayStart()
}

// setTerminateHandler records the teardown to run when macOS terminates the app
// through its delegate (⌘Q, Dock Quit, logout). Called from startTray before the
// tray — and therefore the delegate — exists.
func setTerminateHandler(h func()) { terminateHandler = h }
