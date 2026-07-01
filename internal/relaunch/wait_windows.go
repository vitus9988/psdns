//go:build windows

package relaunch

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

func waitForProcessExit(pid int, timeout time.Duration) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil // already gone or inaccessible; let the relaunch continue
	}
	defer windows.CloseHandle(h)

	ms := uint32(timeout / time.Millisecond)
	if ms == 0 {
		ms = 1
	}
	status, err := windows.WaitForSingleObject(h, ms)
	if err != nil {
		return err
	}
	if status == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("timed out waiting for process %d to exit", pid)
	}
	return nil
}
