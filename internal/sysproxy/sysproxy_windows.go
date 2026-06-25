//go:build windows

package sysproxy

import (
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows uses the per-user WinINET settings under HKCU (no admin rights needed).
// A registry change alone is not picked up by running browsers, so apply/restore
// also poke wininet.dll's InternetSetOptionW to make WinINET reload. Chrome/Edge/
// IE honor these settings; Firefox uses its own and is unaffected.

const inetSettingsPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

func supported() bool { return true }

func capture() (Backup, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettingsPath, registry.QUERY_VALUE)
	if err != nil {
		return Backup{}, fmt.Errorf("시스템 프록시 설정을 읽지 못했어요: %v", err)
	}
	defer k.Close()

	w := &windowsBackup{}
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		w.ProxyEnable = uint32(v)
		w.ProxyEnableExisted = true
	}
	if v, _, err := k.GetStringValue("ProxyServer"); err == nil {
		w.ProxyServer = v
		w.ProxyServerExisted = true
	}
	if v, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		w.ProxyOverride = v
		w.ProxyOverrideExisted = true
	}
	return Backup{Windows: w}, nil
}

func apply(s Settings) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettingsPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("시스템 프록시 설정을 열지 못했어요: %v", err)
	}
	defer k.Close()

	if err := k.SetStringValue("ProxyServer", formatProxyServer(s.Host, s.Port)); err != nil {
		return fmt.Errorf("시스템 프록시 주소 설정에 실패했어요: %v", err)
	}
	if err := k.SetStringValue("ProxyOverride", formatProxyOverride(s.Bypass)); err != nil {
		return fmt.Errorf("시스템 프록시 예외 설정에 실패했어요: %v", err)
	}
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("시스템 프록시 사용 설정에 실패했어요: %v", err)
	}
	notifyWinInetChanged()
	return nil
}

func restore(b Backup) error {
	if b.Windows == nil {
		return nil
	}
	w := b.Windows
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettingsPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("시스템 프록시 설정을 열지 못했어요: %v", err)
	}
	defer k.Close()

	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Restore ProxyEnable first so a later failure can never leave the proxy
	// enabled while pointing at a now-closed listener.
	if w.ProxyEnableExisted {
		record(k.SetDWordValue("ProxyEnable", w.ProxyEnable))
	} else {
		_ = k.DeleteValue("ProxyEnable") // absent before: delete; not-found is fine
	}
	record(restoreString(k, "ProxyServer", w.ProxyServer, w.ProxyServerExisted))
	record(restoreString(k, "ProxyOverride", w.ProxyOverride, w.ProxyOverrideExisted))
	notifyWinInetChanged()
	if firstErr != nil {
		// Returning an error keeps the on-disk backup so a later restore (or the
		// next launch's RecoverStale) retries rather than leaving a dead proxy.
		return fmt.Errorf("시스템 프록시 원복에 실패했어요: %v", firstErr)
	}
	return nil
}

// restoreString sets name back to val, or deletes it when it did not exist
// before (so a value we created is removed rather than left empty). Delete of an
// absent value is treated as success.
func restoreString(k registry.Key, name, val string, existed bool) error {
	if existed {
		return k.SetStringValue(name, val)
	}
	_ = k.DeleteValue(name)
	return nil
}

var (
	wininet               = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOption = wininet.NewProc("InternetSetOptionW")
)

const (
	internetOptionRefresh         = 37 // INTERNET_OPTION_REFRESH
	internetOptionSettingsChanged = 39 // INTERNET_OPTION_SETTINGS_CHANGED
)

// notifyWinInetChanged tells running WinINET clients to reload proxy settings;
// without it a registry change is not seen until restart. Best-effort: the
// registry is already written, so a failure here is non-fatal.
func notifyWinInetChanged() {
	_, _, _ = procInternetSetOption.Call(0, internetOptionSettingsChanged, 0, 0)
	_, _, _ = procInternetSetOption.Call(0, internetOptionRefresh, 0, 0)
}
