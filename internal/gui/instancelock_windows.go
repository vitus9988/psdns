//go:build windows

package gui

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on f without blocking. held is false when
// another process holds it (ERROR_LOCK_VIOLATION). Windows releases a file lock
// when the handle closes — including on an abrupt process death — so a crashed
// instance does not lock out the next launch.
func lockFile(f *os.File) (held bool, err error) {
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
