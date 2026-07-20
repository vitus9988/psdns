//go:build darwin || linux

package relaunch

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// deadReapedPID spawns a short-lived process, waits for it to fully exit and be
// reaped, then returns its now-defunct pid. A reaped pid no longer exists, so
// syscall.Kill reports it as gone.
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

func TestProcessExists(t *testing.T) {
	if !processExists(os.Getpid()) {
		t.Fatal("processExists(self) = false, want true")
	}
	if processExists(deadReapedPID(t)) {
		t.Fatal("processExists(dead reaped pid) = true, want false")
	}
}

func TestWaitForProcessExitReturnsWhenGone(t *testing.T) {
	if err := waitForProcessExit(deadReapedPID(t), 5*time.Second); err != nil {
		t.Fatalf("waitForProcessExit(dead pid) = %v, want nil", err)
	}
}

func TestWaitForProcessExitTimesOut(t *testing.T) {
	// The process (ourselves) never exits within the timeout, so the poll loop
	// must give up and report the timeout.
	err := waitForProcessExit(os.Getpid(), 150*time.Millisecond)
	if err == nil {
		t.Fatal("waitForProcessExit(self) = nil, want timeout error")
	}
}

func TestRunHappyPath(t *testing.T) {
	// The target pid is already dead, so waitForProcessExit returns immediately.
	// Run then re-execs os.Executable() (this test binary) with a -test.run
	// pattern that matches nothing, so the child exits without running any test
	// (no recursion). Run only calls cmd.Start(), so it never blocks.
	pid := deadReapedPID(t)
	args := Args(pid, []string{"-test.run=TestNoSuchTestZZZ$", "-test.count=1"})
	handled, err := Run(args)
	if !handled || err != nil {
		t.Fatalf("Run(happy path) = handled=%v err=%v, want handled=true err=nil", handled, err)
	}
}
