//go:build darwin || linux

package gui

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on f without blocking. held is false
// when another process holds it (EWOULDBLOCK). The lock is tied to the open file
// description, so it is released by closing f — including when the process dies
// without running any cleanup, which is what makes a crashed instance not lock
// out the next launch.
func lockFile(f *os.File) (held bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
