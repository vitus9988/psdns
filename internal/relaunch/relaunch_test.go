package relaunch

import "testing"

func TestArgsAndParseRoundTrip(t *testing.T) {
	args := Args(1234, []string{"--foo", "bar"})
	req, ok, err := Parse(args)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !ok {
		t.Fatal("Parse did not recognise relaunch args")
	}
	if req.PID != 1234 {
		t.Fatalf("PID = %d, want 1234", req.PID)
	}
	if len(req.Args) != 2 || req.Args[0] != "--foo" || req.Args[1] != "bar" {
		t.Fatalf("Args = %#v, want original args", req.Args)
	}
}

func TestParseIgnoresNormalArgs(t *testing.T) {
	if _, ok, err := Parse([]string{"--help"}); ok || err != nil {
		t.Fatalf("normal args parsed as relaunch: ok=%v err=%v", ok, err)
	}
}

func TestParseRejectsBadPID(t *testing.T) {
	for _, args := range [][]string{
		{Flag},
		{Flag, "0"},
		{Flag, "not-a-pid"},
	} {
		if _, ok, err := Parse(args); !ok || err == nil {
			t.Fatalf("Parse(%v) = ok=%v err=%v, want handled error", args, ok, err)
		}
	}
}
