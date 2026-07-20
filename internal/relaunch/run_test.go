package relaunch

import "testing"

// Platform-dependent Run/wait tests (processExists, deadReapedPID) live in
// run_unix_test.go; only cross-platform argument handling is tested here.

func TestRunIgnoresNonRelaunchArgs(t *testing.T) {
	for _, args := range [][]string{nil, {"x"}} {
		handled, err := Run(args)
		if handled || err != nil {
			t.Fatalf("Run(%v) = handled=%v err=%v, want handled=false err=nil", args, handled, err)
		}
	}
}

func TestRunReportsParseError(t *testing.T) {
	// Recognised as a relaunch invocation (handled=true) but missing the pid.
	handled, err := Run([]string{Flag})
	if !handled || err == nil {
		t.Fatalf("Run(%v) = handled=%v err=%v, want handled=true with error", []string{Flag}, handled, err)
	}
}
