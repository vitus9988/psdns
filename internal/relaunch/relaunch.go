// Package relaunch implements the GUI's post-update relaunch handoff.
package relaunch

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

const (
	// Flag marks an internal helper invocation. It is intentionally not part of
	// the public CLI; cmd/psdns-gui consumes it before Wails starts.
	Flag = "--psdns-relaunch-after-pid"

	defaultWaitTimeout = 30 * time.Second
)

// Request is a parsed relaunch-helper invocation.
type Request struct {
	PID  int
	Args []string
}

// Args builds the argv tail that asks a fresh copy of psdns-gui to wait for pid
// to exit, then start the real GUI with originalArgs.
func Args(pid int, originalArgs []string) []string {
	out := []string{Flag, strconv.Itoa(pid), "--"}
	return append(out, originalArgs...)
}

// Parse returns an internal relaunch request when args starts with Flag.
func Parse(args []string) (Request, bool, error) {
	if len(args) == 0 || args[0] != Flag {
		return Request{}, false, nil
	}
	if len(args) < 2 {
		return Request{}, true, fmt.Errorf("%s requires a pid", Flag)
	}
	pid, err := strconv.Atoi(args[1])
	if err != nil || pid <= 0 {
		return Request{}, true, fmt.Errorf("%s requires a positive pid", Flag)
	}
	rest := args[2:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	return Request{PID: pid, Args: append([]string(nil), rest...)}, true, nil
}

// Run executes a relaunch helper invocation. When args is not a relaunch request
// it returns handled=false.
func Run(args []string) (handled bool, err error) {
	req, ok, err := Parse(args)
	if !ok || err != nil {
		return ok, err
	}
	if err := waitForProcessExit(req.PID, defaultWaitTimeout); err != nil {
		return true, err
	}
	exe, err := os.Executable()
	if err != nil {
		return true, err
	}
	cmd := exec.Command(exe, req.Args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return true, cmd.Start()
}
