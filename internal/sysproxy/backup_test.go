package sysproxy

import (
	"os"
	"path/filepath"
	"reflect"
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
