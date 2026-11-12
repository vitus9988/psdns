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
	// services and perService model a machine with more than one network
	// service, which is where Apply writes to all of them and a per-service
	// difference matters. Both are optional; empty means the single-Wi-Fi
	// machine above and webProxy for every service.
	services   []string
	perService map[string]string
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
			out := "An asterisk (*) denotes that a network service is disabled.\n"
			if len(f.services) == 0 {
				return []byte(out + "Wi-Fi\n*Bluetooth PAN\n"), nil
			}
			return []byte(out + strings.Join(f.services, "\n") + "\n"), nil
		case "-getwebproxy", "-getsecurewebproxy":
			if state, ok := f.perService[args[1]]; ok {
				return []byte(state), nil
			}
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
	if _, err := Apply(s); err != nil {
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
	if _, err := Apply(s); err != nil {
		t.Fatalf("Apply(second): %v", err)
	}
	if got := fake.countCalls("-listallnetworkservices"); got != before+1 {
		t.Errorf("second Apply made %d extra list calls, want 1 (no re-capture)", got-before)
	}

	// Restore puts the captured (disabled) state back and removes the backup.
	restored, err := Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !restored {
		t.Error("Restore must report that it put a backup back")
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

	// Restore with no backup changes nothing and says so, which is what lets the
	// GUI tell "nothing was owed" from "the snapshot went missing".
	restored, err = Restore()
	if err != nil {
		t.Errorf("Restore(again): %v", err)
	}
	if restored {
		t.Error("Restore without a backup must report restored=false")
	}
}

// TestDisableApplied covers the no-backup fallback: it must switch the proxy off
// on every service without needing (or touching) a backup.
func TestDisableApplied(t *testing.T) {
	redirectConfigDir(t)
	fake := &fakeNetworksetup{}
	fake.install(t)

	if err := DisableApplied(); err != nil {
		t.Fatalf("DisableApplied: %v", err)
	}
	for _, want := range [][]string{
		{"-setwebproxystate", "Wi-Fi", "off"},
		{"-setsecurewebproxystate", "Wi-Fi", "off"},
	} {
		found := false
		for _, c := range fake.calls {
			if reflect.DeepEqual(c, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("DisableApplied never ran %v; calls: %v", want, fake.calls)
		}
	}
	if backupExists() {
		t.Error("DisableApplied must not write a backup")
	}
}

func TestRecoverStaleRestoresMatchingOS(t *testing.T) {
	redirectConfigDir(t)
	// The fake's current state still shows exactly what the crashed run applied,
	// so RecoverStale's user-change guard lets the restore proceed.
	fake := &fakeNetworksetup{webProxy: "Enabled: Yes\nServer: 127.0.0.1\nPort: 8080\n"}
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
	// switched off and its bypass list cleared. The guard's read-only capture
	// prefixes the call log, so assert just the mutating (-set*) sequence.
	wantCalls := [][]string{
		{"-setwebproxy", "Wi-Fi", "10.0.0.1", "3128"},
		{"-setwebproxystate", "Wi-Fi", "on"},
		{"-setsecurewebproxystate", "Wi-Fi", "off"},
		{"-setproxybypassdomains", "Wi-Fi", "*.local"},
		{"-setwebproxystate", "Thunderbolt Bridge", "off"},
		{"-setsecurewebproxystate", "Thunderbolt Bridge", "off"},
		{"-setproxybypassdomains", "Thunderbolt Bridge", "Empty"},
	}
	var setCalls [][]string
	for _, c := range fake.calls {
		if strings.HasPrefix(c[0], "-set") {
			setCalls = append(setCalls, c)
		}
	}
	if !reflect.DeepEqual(setCalls, wantCalls) {
		t.Errorf("restore sequence:\n got %v\nwant %v", setCalls, wantCalls)
	}
}

// TestRecoverStaleRestoresWhenOnlyOneServiceWasFixed is the multi-service case:
// after a crash the user repointed Wi-Fi at their own proxy but Ethernet still
// holds psdns's dead address. The cleanup is still owed — dropping the backup
// here would leave Ethernet pointing at nothing with no recovery data left.
func TestRecoverStaleRestoresWhenOnlyOneServiceWasFixed(t *testing.T) {
	redirectConfigDir(t)
	fake := &fakeNetworksetup{
		services: []string{"Wi-Fi", "Ethernet"},
		perService: map[string]string{
			"Wi-Fi":    "Enabled: Yes\nServer: 10.0.0.9\nPort: 3128\n",  // user's own, hand-fixed
			"Ethernet": "Enabled: Yes\nServer: 127.0.0.1\nPort: 8080\n", // still ours
		},
	}
	fake.install(t)

	b := Backup{
		Version:      backupVersion,
		OS:           runtime.GOOS,
		AppliedProxy: "127.0.0.1:8080",
		Darwin: &darwinBackup{Services: []darwinService{
			{Name: "Wi-Fi"},
			{Name: "Ethernet"},
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
		t.Fatal("a service still holding our address means the cleanup is still owed")
	}
	if backupExists() {
		t.Error("recovered backup must be deleted")
	}
}

// TestRecoverStaleDropsBackupWhenNoServiceIsOurs is the other half: nothing in
// the OS points at us any more, so the user's setting is respected and only the
// backup is dropped.
func TestRecoverStaleDropsBackupWhenNoServiceIsOurs(t *testing.T) {
	redirectConfigDir(t)
	fake := &fakeNetworksetup{
		services: []string{"Wi-Fi", "Ethernet"},
		webProxy: "Enabled: Yes\nServer: 10.0.0.9\nPort: 3128\n",
	}
	fake.install(t)

	b := Backup{
		Version:      backupVersion,
		OS:           runtime.GOOS,
		AppliedProxy: "127.0.0.1:8080",
		Darwin:       &darwinBackup{Services: []darwinService{{Name: "Wi-Fi"}, {Name: "Ethernet"}}},
	}
	if err := writeBackup(b); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}
	recovered, err := RecoverStale()
	if err != nil {
		t.Fatalf("RecoverStale: %v", err)
	}
	if recovered {
		t.Fatal("must not clobber a setting the user changed after the crash")
	}
	if backupExists() {
		t.Error("the stale backup must be dropped")
	}
	for _, c := range fake.calls {
		if strings.HasPrefix(c[0], "-set") {
			t.Fatalf("nothing may be written when the user's change is respected, got %v", c)
		}
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

	touched, err := Apply(Settings{Host: "127.0.0.1", Port: 8080})
	if err == nil {
		t.Fatal("Apply must surface the networksetup failure")
	}
	// The backup was written before the OS write failed, so a restore is owed.
	if !touched {
		t.Error("a failure after the snapshot must report touched=true")
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

	_, err := Apply(Settings{Host: "127.0.0.1", Port: 8080})
	if err == nil || !strings.Contains(err.Error(), "실패") {
		t.Errorf("generic failure message expected, got %v", err)
	}
}

func TestApplyCaptureFails(t *testing.T) {
	redirectConfigDir(t)
	fake := &fakeNetworksetup{failCmd: "-listallnetworkservices", failOut: "boom"}
	fake.install(t)

	touched, err := Apply(Settings{Host: "127.0.0.1", Port: 8080})
	if err == nil {
		t.Fatal("Apply must fail when the service list is unavailable")
	}
	// Nothing was changed, so the caller must not think it owes a restore.
	if touched {
		t.Error("a capture failure must report touched=false")
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
