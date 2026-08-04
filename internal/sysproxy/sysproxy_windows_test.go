//go:build windows

package sysproxy

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// useScratchKey points the WinINET helpers at a throwaway HKCU subkey, so these
// tests exercise the real registry code without touching the user's (or the CI
// runner's) actual proxy settings.
func useScratchKey(t *testing.T) registry.Key {
	t.Helper()
	const path = `Software\psdns-test\Internet Settings`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	prev := inetSettingsPath
	inetSettingsPath = path
	t.Cleanup(func() {
		inetSettingsPath = prev
		_ = k.Close()
		_ = registry.DeleteKey(registry.CURRENT_USER, path)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\psdns-test`)
	})
	return k
}

func readValues(t *testing.T) (enable uint64, enableOK bool, server string, serverOK bool) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettingsPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("OpenKey: %v", err)
	}
	defer k.Close()
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		enable, enableOK = v, true
	}
	if v, _, err := k.GetStringValue("ProxyServer"); err == nil {
		server, serverOK = v, true
	}
	return
}

// TestWindowsApplyRestoreFromClean covers the common case: nothing was set
// before, so restore must remove the values we created rather than leave
// ProxyEnable behind (that leftover is what strands the user on a dead proxy).
func TestWindowsApplyRestoreFromClean(t *testing.T) {
	redirectConfigDir(t)
	useScratchKey(t)

	s := Settings{Host: "127.0.0.1", Port: 8080, Bypass: DefaultBypass()}
	if err := Apply(s); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	enable, ok, server, serverOK := readValues(t)
	if !ok || enable != 1 {
		t.Fatalf("ProxyEnable = %d (present=%v), want 1", enable, ok)
	}
	if !serverOK || server != formatProxyServer(s.Host, s.Port) {
		t.Fatalf("ProxyServer = %q, want %q", server, formatProxyServer(s.Host, s.Port))
	}

	restored, err := Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !restored {
		t.Fatal("Restore must report it put the snapshot back")
	}
	if _, ok, _, serverOK := readValues(t); ok || serverOK {
		t.Fatal("values we created must be deleted, not left set")
	}
	if backupExists() {
		t.Error("Restore must delete the backup")
	}
}

// TestWindowsRestoreKeepsPreexistingProxy: a proxy the user already had must come
// back exactly as it was.
func TestWindowsRestoreKeepsPreexistingProxy(t *testing.T) {
	redirectConfigDir(t)
	k := useScratchKey(t)
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		t.Fatalf("seed ProxyEnable: %v", err)
	}
	if err := k.SetStringValue("ProxyServer", "http=10.0.0.9:3128;https=10.0.0.9:3128"); err != nil {
		t.Fatalf("seed ProxyServer: %v", err)
	}

	if err := Apply(Settings{Host: "127.0.0.1", Port: 8080}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	enable, ok, server, _ := readValues(t)
	if !ok || enable != 1 || server != "http=10.0.0.9:3128;https=10.0.0.9:3128" {
		t.Fatalf("pre-existing proxy not restored: enable=%d server=%q", enable, server)
	}
}

// TestWindowsDisableApplied covers the no-backup fallback: ProxyEnable must go to
// 0 so traffic stops being routed through a listener that is going away.
func TestWindowsDisableApplied(t *testing.T) {
	redirectConfigDir(t)
	useScratchKey(t)
	if err := Apply(Settings{Host: "127.0.0.1", Port: 8080}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := DisableApplied(); err != nil {
		t.Fatalf("DisableApplied: %v", err)
	}
	if enable, ok, _, _ := readValues(t); !ok || enable != 0 {
		t.Fatalf("ProxyEnable = %d (present=%v), want 0", enable, ok)
	}
}

// TestWindowsDeleteValue: an already-absent value is success, and deleting a
// present one works. Anything else must be reported, since a swallowed failure
// used to be recorded as a successful restore.
func TestWindowsDeleteValue(t *testing.T) {
	k := useScratchKey(t)
	if err := deleteValue(k, "NoSuchValue"); err != nil {
		t.Fatalf("deleting an absent value must succeed, got %v", err)
	}
	if err := k.SetStringValue("Temp", "x"); err != nil {
		t.Fatalf("SetStringValue: %v", err)
	}
	if err := deleteValue(k, "Temp"); err != nil {
		t.Fatalf("deleteValue: %v", err)
	}
	if _, _, err := k.GetStringValue("Temp"); err == nil {
		t.Fatal("value still present after deleteValue")
	}
}

// TestWindowsCapture reads the values back through the production capture path.
func TestWindowsCapture(t *testing.T) {
	k := useScratchKey(t)
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := k.SetStringValue("ProxyServer", "http=127.0.0.1:8080"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	b, err := capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if b.Windows == nil || !b.Windows.ProxyEnableExisted || b.Windows.ProxyEnable != 1 {
		t.Fatalf("capture missed ProxyEnable: %+v", b.Windows)
	}
	if got := currentFromBackup(b); !got.Enabled || got.Host != "127.0.0.1" || got.Port != 8080 {
		t.Fatalf("currentFromBackup = %+v", got)
	}
}
