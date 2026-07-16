package main

import (
	"os"
	"os/exec"
	"testing"

	"github.com/vitus9988/psdns/internal/gui"
	"github.com/vitus9988/psdns/internal/relaunch"
	"github.com/vitus9988/psdns/internal/selfupdate"
)

// deadReapedPID spawns a short-lived process, waits for it to fully exit and be
// reaped, then returns its now-defunct pid. A reaped pid no longer exists, so
// relaunch's process-exit poll returns immediately without waiting.
func deadReapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start /usr/bin/true: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait /usr/bin/true: %v", err)
	}
	return cmd.Process.Pid
}

// relaunchArgs builds a relaunch tail whose re-exec of this test binary runs a
// -test.run pattern matching nothing, so the child exits instantly without
// recursing.
func relaunchArgs(t *testing.T) []string {
	t.Helper()
	return relaunch.Args(deadReapedPID(t), []string{"-test.run=TestNoSuchTestZZZ$", "-test.count=1"})
}

func TestDisplayVersion(t *testing.T) {
	origVersion := version
	origSelfupdate := selfupdate.Version
	t.Cleanup(func() {
		version = origVersion
		selfupdate.Version = origSelfupdate
	})

	cases := []struct {
		name       string
		version    string
		selfupdate string
		want       string
	}{
		{"dev falls back to selfupdate", "dev", "v9.9.9", "v9.9.9"},
		{"both dev stays dev", "dev", "dev", "dev"},
		{"injected version wins", "v1.2.3", "v0.0.1", "v1.2.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			version = tc.version
			selfupdate.Version = tc.selfupdate
			if got := displayVersion(); got != tc.want {
				t.Fatalf("displayVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAppOptions(t *testing.T) {
	opts := appOptions(gui.NewApp("t"))

	if opts.Title != "psdns" {
		t.Errorf("Title = %q, want %q", opts.Title, "psdns")
	}
	if opts.Width != 480 {
		t.Errorf("Width = %d, want 480", opts.Width)
	}
	if opts.Height != 860 {
		t.Errorf("Height = %d, want 860", opts.Height)
	}
	if len(opts.Bind) != 1 {
		t.Errorf("len(Bind) = %d, want 1", len(opts.Bind))
	}
	if opts.OnStartup == nil {
		t.Error("OnStartup = nil, want non-nil")
	}
	if opts.OnShutdown == nil {
		t.Error("OnShutdown = nil, want non-nil")
	}
	if opts.OnBeforeClose == nil {
		t.Error("OnBeforeClose = nil, want non-nil")
	}
	if opts.SingleInstanceLock == nil {
		t.Fatal("SingleInstanceLock = nil, want non-nil")
	}
	if got := opts.SingleInstanceLock.UniqueId; got != "psdns-gui.vitus9988.github.io" {
		t.Errorf("SingleInstanceLock.UniqueId = %q, want %q", got, "psdns-gui.vitus9988.github.io")
	}
}

func TestRunRelaunchHandled(t *testing.T) {
	// A relaunch invocation is handled entirely by relaunch.Run, so run returns
	// without ever reaching wails.Run. The target pid is already dead, so the
	// exit poll returns immediately, and relaunch only Start()s the child.
	if err := run(relaunchArgs(t)); err != nil {
		t.Fatalf("run(relaunch args) = %v, want nil", err)
	}
}

func TestMainRelaunchHandled(t *testing.T) {
	// main delegates to run with os.Args[1:]; a relaunch invocation returns nil,
	// so logFatal must not fire and main returns normally (never reaching Wails).
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })
	os.Args = append([]string{"psdns-gui"}, relaunchArgs(t)...)

	origFatal := logFatal
	t.Cleanup(func() { logFatal = origFatal })
	logFatal = func(v ...interface{}) {
		t.Fatalf("logFatal called on happy path: %v", v)
	}

	main()
}

func TestMainFatalBranch(t *testing.T) {
	// A relaunch flag with no pid is recognised (handled=true) but errors out
	// immediately, so run returns an error and main routes it to logFatal. The
	// override records the error instead of exiting the test process.
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })
	os.Args = []string{"psdns-gui", relaunch.Flag}

	origFatal := logFatal
	t.Cleanup(func() { logFatal = origFatal })
	var got []interface{}
	logFatal = func(v ...interface{}) { got = v }

	main()

	if len(got) != 1 {
		t.Fatalf("logFatal args = %v, want exactly one error", got)
	}
	if err, ok := got[0].(error); !ok || err == nil {
		t.Fatalf("logFatal arg = %v (%T), want non-nil error", got[0], got[0])
	}
}
