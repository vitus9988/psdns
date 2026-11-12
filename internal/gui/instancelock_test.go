package gui

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// useTempLock points the instance lock at a fresh temp file for the test.
func useTempLock(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "instance.lock")
	prev := instanceLockPath
	instanceLockPath = func() (string, error) { return p, nil }
	t.Cleanup(func() { instanceLockPath = prev })
	return p
}

// recordExit swaps the exit seam for a recorder so a "second instance" path can
// be tested without ending the test binary.
func recordExit(t *testing.T) *[]int {
	t.Helper()
	var codes []int
	prev := exitFn
	exitFn = func(code int) { codes = append(codes, code) }
	t.Cleanup(func() { exitFn = prev })
	return &codes
}

// TestInstanceLockExcludesSecondHolder is the core guarantee: while one App
// holds the lock a second cannot take it, and once the first releases it the
// second can.
func TestInstanceLockExcludesSecondHolder(t *testing.T) {
	useTempLock(t)

	first := &App{}
	if !first.acquireInstanceLock() {
		t.Fatal("first acquire must succeed")
	}
	second := &App{}
	if second.acquireInstanceLock() {
		t.Fatal("second acquire must fail while the first holds the lock")
	}
	first.releaseInstanceLock()
	if !second.acquireInstanceLock() {
		t.Fatal("acquire must succeed after the holder released")
	}
	second.releaseInstanceLock()
}

// TestReleaseInstanceLockIdempotent: releasing twice (Shutdown can run after a
// Quit already tore things down) is harmless.
func TestReleaseInstanceLockIdempotent(t *testing.T) {
	useTempLock(t)
	a := &App{}
	if !a.acquireInstanceLock() {
		t.Fatal("acquire must succeed")
	}
	a.releaseInstanceLock()
	a.releaseInstanceLock()
}

// TestAcquireInstanceLockFailsOpen: a lock path that cannot be evaluated must
// not block startup — the guard is a safety net, not a gate.
func TestAcquireInstanceLockFailsOpen(t *testing.T) {
	prev := instanceLockPath
	instanceLockPath = func() (string, error) { return "", errors.New("no config dir") }
	t.Cleanup(func() { instanceLockPath = prev })

	a := &App{}
	if !a.acquireInstanceLock() {
		t.Fatal("an unusable lock path must fall open to starting")
	}
	if a.instanceLock != nil {
		t.Fatal("no lock file should be held when the guard was skipped")
	}
}

// TestStartupRefusesSecondInstance covers the Startup guard: the duplicate exits
// and — crucially — never reaches RecoverStale, which would otherwise restore
// and delete the live instance's OS-proxy backup.
func TestStartupRefusesSecondInstance(t *testing.T) {
	redirectConfigDir(t)
	useTempLock(t)
	codes := recordExit(t)

	holder := &App{}
	if !holder.acquireInstanceLock() {
		t.Fatal("holder acquire must succeed")
	}
	t.Cleanup(holder.releaseInstanceLock)

	recovered := false
	prev := sysproxyRecoverStale
	sysproxyRecoverStale = func() (bool, error) { recovered = true; return false, nil }
	t.Cleanup(func() { sysproxyRecoverStale = prev })

	dup := NewApp("t")
	dup.updater = stubErrChecker()
	dup.Startup(context.Background())

	if recovered {
		t.Fatal("a second instance must not run stale-proxy cleanup")
	}
	if len(*codes) != 1 || (*codes)[0] != 0 {
		t.Fatalf("want a single exit(0), got %v", *codes)
	}
	if !dup.secondInstance.Load() {
		t.Fatal("secondInstance must be recorded")
	}
	if _, err := dup.Start("proxy"); err == nil {
		t.Fatal("Start must refuse in a second instance")
	}
}
