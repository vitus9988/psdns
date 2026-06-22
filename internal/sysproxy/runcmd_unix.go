//go:build darwin || linux

package sysproxy

import "os/exec"

// runCmd runs name with args and returns combined stdout+stderr. It is a package
// variable so tests can stub it and assert the command sequence without spawning
// real processes (and so a friendly error can read the tool's stderr).
var runCmd = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// lookCmd reports whether a command is on PATH (used by Supported()).
func lookCmd(name string) (string, error) { return exec.LookPath(name) }
