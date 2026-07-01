package gui

import (
	"context"
	"testing"
)

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

func TestRuntimeContextHelpers(t *testing.T) {
	a := &App{}
	ctx := context.Background()
	a.setRuntimeContext(ctx)
	if got := a.runtimeContext(); got != ctx {
		t.Fatal("runtimeContext did not return the stored context")
	}
	a.setRuntimeContext(nil)
	if got := a.runtimeContext(); got != nil {
		t.Fatal("runtimeContext should return nil after clearing")
	}
}

func TestCancelBackgroundCheckClearsAndRunsOnce(t *testing.T) {
	a := &App{}
	called := 0
	a.setBackgroundCancel(func() { called++ })
	a.cancelBackgroundCheck()
	a.cancelBackgroundCheck()
	if called != 1 {
		t.Fatalf("cancel called %d times, want 1", called)
	}
}
