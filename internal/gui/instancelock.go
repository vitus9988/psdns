// instancelock.go keeps a second psdns-gui process from running alongside a
// live one. Wails has its own SingleInstanceLock, and it stays wired up as the
// mechanism that reveals the existing window — but it is not airtight: on Linux
// Wails starts OnStartup concurrently with its lock check, so a doomed second
// instance still runs App.Startup (and its RecoverStale, which would restore and
// delete the live instance's sysproxy backup out from under it); on Windows a
// CreateMutex error other than ERROR_ALREADY_EXISTS (elevated vs non-elevated)
// starts a full second instance; and macOS locks a file in NSTemporaryDirectory,
// which differs per launch context (e.g. sudo), so two instances can miss each
// other. A duplicate is also how users hit "port already in use": the second
// process fights the first for :53, which has no port fallback.
//
// The lock file lives next to the sysproxy backup in <UserConfigDir>/psdns, so
// the lock's scope matches the backup's: two processes that share one backup
// file necessarily see the same lock, and two that don't (a different HOME under
// sudo) cannot corrupt each other's backup anyway.
package gui

import (
	"log"
	"os"
	"path/filepath"
)

// Test seams: the real implementations lock a real file and terminate the
// process, neither of which a unit test can tolerate. Defaults are the real
// implementations.
var (
	instanceLockPath = defaultInstanceLockPath
	exitFn           = os.Exit
)

// defaultInstanceLockPath is <UserConfigDir>/psdns/instance.lock — the same
// directory as sysproxy's backup, by design (see the package comment).
func defaultInstanceLockPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "psdns", "instance.lock"), nil
}

// acquireInstanceLock takes the single-instance lock without blocking. It
// reports false only when another live process holds it; if the lock cannot be
// evaluated at all (no config dir, unwritable path) it falls open to true, since
// refusing to start over a bookkeeping failure would be worse than the duplicate
// it guards against.
func (a *App) acquireInstanceLock() bool {
	p, err := instanceLockPath()
	if err != nil {
		log.Printf("gui: instance lock path unavailable, skipping guard: %v", err)
		return true
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		log.Printf("gui: instance lock dir unavailable, skipping guard: %v", err)
		return true
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		log.Printf("gui: instance lock file unavailable, skipping guard: %v", err)
		return true
	}
	held, err := lockFile(f)
	if err != nil {
		_ = f.Close()
		log.Printf("gui: instance lock unavailable, skipping guard: %v", err)
		return true
	}
	if !held {
		_ = f.Close()
		return false
	}
	a.instanceLock = f
	return true
}

// releaseInstanceLock drops the lock on shutdown. Closing the file releases the
// OS lock on every platform, and the file itself is left behind on purpose: it
// is a lock handle, not state, and recreating it each run would race.
func (a *App) releaseInstanceLock() {
	if a.instanceLock == nil {
		return
	}
	unlockFile(a.instanceLock)
	_ = a.instanceLock.Close()
	a.instanceLock = nil
}

// refuseSecondInstance ends a duplicate launch. Nothing has been applied or
// bound at this point — the guard runs before the tray, before RecoverStale and
// long before any listener — so exiting immediately is safe and leaves the live
// instance's OS proxy backup untouched. The exit is quiet, matching Wails's own
// single-instance handlers (which also just exit and let the first instance
// surface itself).
func (a *App) refuseSecondInstance() {
	log.Print("gui: another psdns is already running; exiting this duplicate")
	exitFn(0)
}
