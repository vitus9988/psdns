package gui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vitus9988/psdns/internal/config"
	"github.com/vitus9988/psdns/internal/supervisor"
	"github.com/vitus9988/psdns/internal/uiconfig"
)

// TestAllListenersFailed covers the pure decision: a session counts as failed
// only when it has listeners and none of them came up.
func TestAllListenersFailed(t *testing.T) {
	tests := []struct {
		name string
		st   supervisor.State
		want bool
	}{
		{"no listeners", supervisor.State{}, false},
		{"one up", supervisor.State{Listeners: []supervisor.Listener{
			{Kind: "http", Up: true},
			{Kind: "dns", Err: "포트가 이미 사용 중이에요."},
		}}, false},
		{"all down", supervisor.State{Listeners: []supervisor.Listener{
			{Kind: "http", Err: "권한이 없어요."},
			{Kind: "socks", Err: "포트가 이미 사용 중이에요."},
		}}, true},
		{"all down without messages", supervisor.State{Listeners: []supervisor.Listener{
			{Kind: "dns"},
		}}, true},
	}
	for _, tc := range tests {
		msg, got := allListenersFailed(tc.st)
		if got != tc.want {
			t.Errorf("%s: allListenersFailed = %v, want %v", tc.name, got, tc.want)
		}
		if got && msg == "" {
			t.Errorf("%s: a failure must carry a message", tc.name)
		}
	}
}

// TestStartRollsBackWhenNothingBound is the reported symptom's UI half: when no
// listener can be bound, Start used to resolve "running" with per-listener errors
// only, leaving the UI showing a live session bound to nothing (and a power
// button that says 중지하기). It must fail and stop the servers instead.
//
// A busy port cannot produce this — the port fallback ends at host:0, which
// always binds — so the test uses an address that belongs to no interface
// (TEST-NET-1), which fails for the requested port, every alternate, and :0.
func TestStartRollsBackWhenNothingBound(t *testing.T) {
	redirectConfigDir(t)

	a := &App{sup: supervisor.New(config.Default())}
	a.setRuntimeContext(context.Background())
	cfg := uiconfig.FromConfig(config.Default())
	cfg.ProxyListen, cfg.SocksListen = "192.0.2.1:8080", "192.0.2.1:1080"
	cfg.SetSystemProxy = false
	if _, err := a.SetConfig(cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	st, err := a.Start("proxy")
	if err == nil {
		t.Fatal("Start must fail when no listener came up")
	}
	if !strings.Contains(err.Error(), "192.0.2.1") {
		t.Fatalf("error should name the address that failed, got %q", err)
	}
	if st.Running {
		t.Fatal("the failed session must be rolled back, not left running")
	}
	if a.sup.Status().Running {
		t.Fatal("supervisor must be stopped after the rollback")
	}
}

// TestStartKeepsPartialSuccess: a bind failure on only some listeners still
// resolves, so the working ones keep serving and the UI reports the rest.
func TestStartKeepsPartialSuccess(t *testing.T) {
	redirectConfigDir(t)
	a := &App{sup: supervisor.New(config.Default())}
	a.setRuntimeContext(context.Background())
	cfg := uiconfig.FromConfig(config.Default())
	// A privileged DNS port fails for an unprivileged test process, while the
	// proxies bind normally (port 0 lets the OS pick).
	cfg.DNSListen = "127.0.0.1:53"
	cfg.ProxyListen, cfg.SocksListen = "127.0.0.1:0", "127.0.0.1:0"
	cfg.SetSystemProxy = false
	if _, err := a.SetConfig(cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	st, err := a.Start("run")
	t.Cleanup(func() { _, _ = a.Stop() })
	if err != nil {
		t.Fatalf("a partial failure must still resolve, got %v", err)
	}
	if !st.Running {
		t.Fatal("the listeners that came up must keep running")
	}
}

// TestStopTranslatesNotRunning keeps supervisor's internal wording out of the UI.
func TestStopTranslatesNotRunning(t *testing.T) {
	a := &App{sup: supervisor.New(config.Default())}
	_, err := a.Stop()
	if err == nil {
		t.Fatal("Stop on an idle supervisor must report something")
	}
	if strings.Contains(err.Error(), "supervisor:") {
		t.Fatalf("internal error text leaked to the UI: %q", err)
	}
	// An error that is not ErrNotRunning passes through untouched, so a real
	// teardown failure is not disguised as "already off".
	other := errors.New("teardown exploded")
	if got := friendlyStopErr(other); !errors.Is(got, other) {
		t.Fatalf("friendlyStopErr must pass other errors through, got %v", got)
	}
}
