package sysproxy

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// redirectConfigDir points os.UserConfigDir at a temp dir on every OS, so the
// backup tests never touch the user's real psdns config.
func redirectConfigDir(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)                                       // darwin (…/Library/Application Support) and unix fallback
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))      // linux
	t.Setenv("AppData", filepath.Join(tmp, "appdata"))          // windows
	t.Setenv("XDG_CONFIG_DIRS", filepath.Join(tmp, "xdg-dirs")) // defensive: never consult system dirs
}

func TestBackupLifecycle(t *testing.T) {
	redirectConfigDir(t)

	if backupExists() {
		t.Fatal("fresh config dir must have no backup")
	}
	b := Backup{
		Version:      backupVersion,
		OS:           "darwin",
		AppliedProxy: "127.0.0.1:8080",
		Darwin: &darwinBackup{Services: []darwinService{
			{Name: "Wi-Fi", WebEnabled: true, WebServer: "10.0.0.1", WebPort: 3128, Bypass: []string{"*.local"}},
		}},
	}
	if err := writeBackup(b); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}
	if !backupExists() {
		t.Fatal("backupExists must report true after writeBackup")
	}
	got, ok, err := readBackup()
	if err != nil || !ok {
		t.Fatalf("readBackup: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, b) {
		t.Errorf("readBackup mismatch:\n got %+v\nwant %+v", got, b)
	}
	if err := deleteBackup(); err != nil {
		t.Fatalf("deleteBackup: %v", err)
	}
	if backupExists() {
		t.Fatal("backup must be gone after deleteBackup")
	}
	// Deleting a missing backup is a no-op, not an error.
	if err := deleteBackup(); err != nil {
		t.Errorf("deleteBackup(again): %v", err)
	}
}

func TestReadBackupCorruptIsDeleted(t *testing.T) {
	redirectConfigDir(t)

	p, err := backupPath()
	if err != nil {
		t.Fatalf("backupPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, ok, err := readBackup()
	if ok || err == nil {
		t.Fatalf("corrupt backup: ok=%v err=%v, want reported absent with error", ok, err)
	}
	if backupExists() {
		t.Error("corrupt backup must be deleted so it cannot wedge startup")
	}
}

func TestRecoverStaleNoBackup(t *testing.T) {
	redirectConfigDir(t)

	recovered, err := RecoverStale()
	if err != nil {
		t.Fatalf("RecoverStale: %v", err)
	}
	if recovered {
		t.Error("RecoverStale must be a no-op without a backup")
	}
}

func TestRecoverStaleForeignOSBackupDiscarded(t *testing.T) {
	redirectConfigDir(t)

	// A backup synced from another OS must be discarded, never applied.
	if err := writeBackup(Backup{Version: backupVersion, OS: "beos"}); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}
	recovered, err := RecoverStale()
	if err != nil {
		t.Fatalf("RecoverStale: %v", err)
	}
	if recovered {
		t.Error("foreign-OS backup must not count as recovered")
	}
	if backupExists() {
		t.Error("foreign-OS backup must be deleted")
	}
}

// swapRecoverSeams replaces the OS capture/restore seams for RecoverStale tests
// and restores them on cleanup. restored records every osRestore call.
func swapRecoverSeams(t *testing.T, capture func() (Backup, error)) (restored *[]Backup) {
	t.Helper()
	origCapture, origRestore := osCapture, osRestore
	t.Cleanup(func() { osCapture, osRestore = origCapture, origRestore })
	osCapture = capture
	var calls []Backup
	osRestore = func(b Backup) error { calls = append(calls, b); return nil }
	return &calls
}

func TestRecoverStaleRestoresWhenStillOurs(t *testing.T) {
	redirectConfigDir(t)
	// The OS proxy still points at what the crashed run applied -> restore it.
	restored := swapRecoverSeams(t, func() (Backup, error) {
		return Backup{Windows: &windowsBackup{ProxyEnable: 1, ProxyServer: "https=127.0.0.1:8080"}}, nil
	})
	if err := writeBackup(Backup{Version: backupVersion, OS: runtime.GOOS, AppliedProxy: "127.0.0.1:8080"}); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}
	recovered, err := RecoverStale()
	if err != nil || !recovered {
		t.Fatalf("RecoverStale = %v, %v; want recovered", recovered, err)
	}
	if len(*restored) != 1 {
		t.Errorf("osRestore calls = %d, want 1", len(*restored))
	}
	if backupExists() {
		t.Error("backup must be deleted after recovery")
	}
}

func TestRecoverStaleRespectsUserChange(t *testing.T) {
	redirectConfigDir(t)
	// Since the crash the user pointed the OS at another proxy (or turned it
	// off): restoring our old snapshot would clobber that, so only drop the backup.
	for name, cur := range map[string]Backup{
		"different proxy": {Windows: &windowsBackup{ProxyEnable: 1, ProxyServer: "https=127.0.0.1:3128"}},
		"proxy disabled":  {Windows: &windowsBackup{ProxyEnable: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			restored := swapRecoverSeams(t, func() (Backup, error) { return cur, nil })
			if err := writeBackup(Backup{Version: backupVersion, OS: runtime.GOOS, AppliedProxy: "127.0.0.1:8080"}); err != nil {
				t.Fatalf("writeBackup: %v", err)
			}
			recovered, err := RecoverStale()
			if err != nil || recovered {
				t.Fatalf("RecoverStale = %v, %v; want not recovered, no error", recovered, err)
			}
			if len(*restored) != 0 {
				t.Error("osRestore must not run when the user changed the proxy")
			}
			if backupExists() {
				t.Error("backup must still be deleted")
			}
		})
	}
}

func TestRecoverStaleLegacyBackupRestores(t *testing.T) {
	redirectConfigDir(t)
	// A backup without AppliedProxy predates the guard: restore as before,
	// without consulting the current OS state at all.
	restored := swapRecoverSeams(t, func() (Backup, error) {
		t.Fatal("legacy backups must not trigger a capture")
		return Backup{}, nil
	})
	if err := writeBackup(Backup{Version: backupVersion, OS: runtime.GOOS}); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}
	recovered, err := RecoverStale()
	if err != nil || !recovered {
		t.Fatalf("RecoverStale = %v, %v; want recovered", recovered, err)
	}
	if len(*restored) != 1 {
		t.Errorf("osRestore calls = %d, want 1", len(*restored))
	}
}

func TestRecoverStaleDetectionErrorFailOpen(t *testing.T) {
	redirectConfigDir(t)
	// When the current state cannot be read, fall open to restoring — the same
	// stance as the conflict guard.
	restored := swapRecoverSeams(t, func() (Backup, error) { return Backup{}, errors.New("boom") })
	if err := writeBackup(Backup{Version: backupVersion, OS: runtime.GOOS, AppliedProxy: "127.0.0.1:8080"}); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}
	recovered, err := RecoverStale()
	if err != nil || !recovered {
		t.Fatalf("RecoverStale = %v, %v; want recovered (fail-open)", recovered, err)
	}
	if len(*restored) != 1 {
		t.Errorf("osRestore calls = %d, want 1", len(*restored))
	}
}

func TestDefaultBypass(t *testing.T) {
	got := DefaultBypass()
	if len(got) == 0 {
		t.Fatal("DefaultBypass must not be empty")
	}
	want := map[string]bool{"localhost": true, "127.0.0.1": true}
	for _, h := range got {
		delete(want, h)
	}
	if len(want) != 0 {
		t.Errorf("DefaultBypass %v is missing %v", got, want)
	}
}
