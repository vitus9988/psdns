package gui

import "testing"

// TestShouldPreventClose covers the window-close decision in isolation from the
// Wails runtime: a fresh app intercepts the close and hides to the tray, and
// once a real quit is in flight (quitting set, as Quit does) the close is
// allowed through so the app can exit.
func TestShouldPreventClose(t *testing.T) {
	a := &App{}
	if !a.shouldPreventClose() {
		t.Fatal("fresh app should prevent close (hide to tray)")
	}
	a.quitting.Store(true)
	if a.shouldPreventClose() {
		t.Fatal("after quitting is set, close should be allowed through")
	}
}
