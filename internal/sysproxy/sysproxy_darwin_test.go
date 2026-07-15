//go:build darwin

package sysproxy

import (
	"errors"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// fakeNetworksetup stubs runCmd with a canned networksetup: one active service
// ("Wi-Fi") whose current proxies are captured from the given state. Every
// invocation is recorded so tests can assert the exact command sequence, and
// failCmd (when non-empty) makes that subcommand fail with failOut as output.
type fakeNetworksetup struct {
	calls    [][]string
	webProxy string // canned -getwebproxy / -getsecurewebproxy output
	failCmd  string
	failOut  string
}

func (f *fakeNetworksetup) install(t *testing.T) {
	t.Helper()
	prev := runCmd
	runCmd = func(name string, args ...string) ([]byte, error) {
		if name != "networksetup" {
			t.Errorf("unexpected command %q %v", name, args)
			return nil, errors.New("unexpected command")
		}
		f.calls = append(f.calls, args)
		if f.failCmd != "" && args[0] == f.failCmd {
			return []byte(f.failOut), errors.New("exit status 14")
		}
		switch args[0] {
		case "-listallnetworkservices":
			return []byte("An asterisk (*) denotes that a network service is disabled.\nWi-Fi\n*Bluetooth PAN\n"), nil
		case "-getwebproxy", "-getsecurewebproxy":
			return []byte(f.webProxy), nil
		case "-getproxybypassdomains":
			return []byte("*.local\n"), nil
		default: // -set* commands
			return nil, nil
		}
	}
	t.Cleanup(func() { runCmd = prev })
}

// countCalls returns how many recorded invocations used the given subcommand.
func (f *fakeNetworksetup) countCalls(sub string) int {
	n := 0
	for _, c := range f.calls {
		if c[0] == sub {
			n++
		}
	}
	return n
}

func TestApplyRestoreRoundTrip(t *testing.T) {
	redirectConfigDir(t)
	fake := &fakeNetworksetup{webProxy: "Enabled: No\nServer:\nPort: 0\n"}
	fake.install(t)

	s := Settings{Host: "127.0.0.1", Port: 8080, Bypass: []string{"localhost", "127.0.0.1"}}
	if err := Apply(s); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !backupExists() {
		t.Fatal("Apply must snapshot the previous state before changing it")
	}
	wantSet := []string{"-setwebproxy", "Wi-Fi", "127.0.0.1", "8080"}
	found := false
	for _, c := range fake.calls {
		if reflect.DeepEqual(c, wantSet) {
			found = true
		}
	}
	if !found {
		t.Errorf("Apply never ran %v; calls: %v", wantSet, fake.calls)
	}

	// A second Apply must not re-capture (the backup would be overwritten with
	// our own values): capture lists services once, apply lists them once more,
	// so a re-capture would show a third -listallnetworkservices per Apply.
	before := fake.countCalls("-listallnetworkservices")
	if before != 2 {
		t.Fatalf("first Apply: %d list calls, want 2 (capture + apply)", before)
	}
	if err := Apply(s); err != nil {
		t.Fatalf("Apply(second): %v", err)
	}
	if got := fake.countCalls("-listallnetworkservices"); got != before+1 {
		t.Errorf("second Apply made %d extra list calls, want 1 (no re-capture)", got-before)
	}

	// Restore puts the captured (disabled) state back and removes the backup.
	if err := Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if backupExists() {
		t.Error("Restore must delete the backup")
	}
	wantOff := []string{"-setwebproxystate", "Wi-Fi", "off"}
	found = false
	for _, c := range fake.calls {
		if reflect.DeepEqual(c, wantOff) {
			found = true
		}
	}
	if !found {
		t.Errorf("Restore never turned the web proxy back off; calls: %v", fake.calls)
	}

	// Restore with no backup is a safe no-op.
	if err := Restore(); err != nil {
		t.Errorf("Restore(again): %v", err)
	}
}

func TestRecoverStaleRestoresMatchingOS(t *testing.T) {
	redirectConfigDir(t)
	fake := &fakeNetworksetup{}
	fake.install(t)

	b := Backup{
		Version:      backupVersion,
		OS:           runtime.GOOS,
		AppliedProxy: "127.0.0.1:8080",
		Darwin: &darwinBackup{Services: []darwinService{
			{Name: "Wi-Fi", WebEnabled: true, WebServer: "10.0.0.1", WebPort: 3128, Bypass: []string{"*.local"}},
			{Name: "Thunderbolt Bridge"},
		}},
	}
	if err := writeBackup(b); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}
	recovered, err := RecoverStale()
	if err != nil {
		t.Fatalf("RecoverStale: %v", err)
	}
	if !recovered {
		t.Fatal("a stale same-OS backup must be recovered")
	}
	if backupExists() {
		t.Error("recovered backup must be deleted")
	}
	// The enabled service is re-pointed at its old server; the untouched one is
	// switched off and its bypass list cleared.
	wantCalls := [][]string{
		{"-setwebproxy", "Wi-Fi", "10.0.0.1", "3128"},
		{"-setwebproxystate", "Wi-Fi", "on"},
		{"-setsecurewebproxystate", "Wi-Fi", "off"},
		{"-setproxybypassdomains", "Wi-Fi", "*.local"},
		{"-setwebproxystate", "Thunderbolt Bridge", "off"},
		{"-setsecurewebproxystate", "Thunderbolt Bridge", "off"},
		{"-setproxybypassdomains", "Thunderbolt Bridge", "Empty"},
	}
	if !reflect.DeepEqual(fake.calls, wantCalls) {
		t.Errorf("restore sequence:\n got %v\nwant %v", fake.calls, wantCalls)
	}
}

func TestApplyAdminRefusalIsFriendly(t *testing.T) {
	redirectConfigDir(t)
	fake := &fakeNetworksetup{
		webProxy: "Enabled: No\nServer:\nPort: 0\n",
		failCmd:  "-setwebproxy",
		failOut:  "requires admin privileges to change proxy settings",
	}
	fake.install(t)

	err := Apply(Settings{Host: "127.0.0.1", Port: 8080})
	if err == nil {
		t.Fatal("Apply must surface the networksetup failure")
	}
	if !strings.Contains(err.Error(), "관리자 권한") {
		t.Errorf("admin refusal must map to the friendly admin message, got %v", err)
	}
	// The failed server-set must stop the sequence: the proxy state is never
	// enabled on top of a server that was not actually set.
	if n := fake.countCalls("-setwebproxystate"); n != 0 {
		t.Errorf("proxy state was enabled %d times after a failed server-set", n)
	}
	// A failed Apply still leaves the backup for the mandatory Restore.
	if !backupExists() {
		t.Error("failed Apply must keep the snapshot for Restore")
	}
}

func TestApplyGenericFailure(t *testing.T) {
	redirectConfigDir(t)
	fake := &fakeNetworksetup{
		webProxy: "Enabled: No\nServer:\nPort: 0\n",
		failCmd:  "-setsecurewebproxy",
		failOut:  "some other failure",
	}
	fake.install(t)

	err := Apply(Settings{Host: "127.0.0.1", Port: 8080})
	if err == nil || !strings.Contains(err.Error(), "실패") {
		t.Errorf("generic failure message expected, got %v", err)
	}
}

func TestApplyCaptureFails(t *testing.T) {
	redirectConfigDir(t)
	fake := &fakeNetworksetup{failCmd: "-listallnetworkservices", failOut: "boom"}
	fake.install(t)

	if err := Apply(Settings{Host: "127.0.0.1", Port: 8080}); err == nil {
		t.Fatal("Apply must fail when the service list is unavailable")
	}
	if backupExists() {
		t.Error("no backup may be written when capture failed")
	}
}

func TestCaptureRecordsProxyState(t *testing.T) {
	fake := &fakeNetworksetup{webProxy: "Enabled: Yes\nServer: 10.0.0.1\nPort: 3128\n"}
	fake.install(t)

	b, err := capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(b.Darwin.Services) != 1 {
		t.Fatalf("captured services: %+v", b.Darwin.Services)
	}
	svc := b.Darwin.Services[0]
	if svc.Name != "Wi-Fi" || !svc.WebEnabled || svc.WebServer != "10.0.0.1" || svc.WebPort != 3128 {
		t.Errorf("captured web proxy state: %+v", svc)
	}
	if !svc.SecureEnabled || !reflect.DeepEqual(svc.Bypass, []string{"*.local"}) {
		t.Errorf("captured secure/bypass state: %+v", svc)
	}
}

func TestCaptureToleratesGetFailures(t *testing.T) {
	fake := &fakeNetworksetup{failCmd: "-getwebproxy", failOut: "boom", webProxy: "Enabled: No\n"}
	fake.install(t)

	b, err := capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	// A failed -getwebproxy degrades to "disabled", it does not abort capture.
	if svc := b.Darwin.Services[0]; svc.WebEnabled || svc.WebServer != "" {
		t.Errorf("failed get must capture a zero state, got %+v", svc)
	}
}

func TestRestoreWithoutDarwinStateIsNoop(t *testing.T) {
	fake := &fakeNetworksetup{}
	fake.install(t)
	if err := restore(Backup{OS: runtime.GOOS}); err != nil {
		t.Fatalf("restore(no darwin state): %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("restore without darwin state ran commands: %v", fake.calls)
	}
}

func TestClassifyDarwinErr(t *testing.T) {
	if got := classifyDarwinErr(nil, nil); got != nil {
		t.Errorf("nil error must stay nil, got %v", got)
	}
	admin := classifyDarwinErr(errors.New("exit status 14"), []byte("You need Admin rights"))
	if !strings.Contains(admin.Error(), "관리자 권한") {
		t.Errorf("admin output must map to the admin message, got %v", admin)
	}
	generic := classifyDarwinErr(errors.New("exit status 1"), []byte("whatever"))
	if !strings.Contains(generic.Error(), "실패") || !strings.Contains(generic.Error(), "whatever") {
		t.Errorf("generic error must include the tool output, got %v", generic)
	}
}

func TestSupportedMatchesPathLookup(t *testing.T) {
	_, lookErr := exec.LookPath("networksetup")
	if got := Supported(); got != (lookErr == nil) {
		t.Errorf("Supported() = %v, but networksetup lookup err = %v", got, lookErr)
	}
}
