package sysproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// backupVersion is the on-disk schema version, for forward compatibility.
const backupVersion = 1

// Backup is the snapshot of the OS proxy state taken before Apply, used to
// restore it later — including after a crash, via RecoverStale. Only the field
// matching the current OS is populated; the OS field guards against applying a
// backup written by a different platform (e.g. a synced config directory).
type Backup struct {
	Version      int            `json:"version"`
	OS           string         `json:"os"`
	AppliedProxy string         `json:"appliedProxy"`
	Windows      *windowsBackup `json:"windows,omitempty"`
	Darwin       *darwinBackup  `json:"darwin,omitempty"`
	Linux        *linuxBackup   `json:"linux,omitempty"`
}

// windowsBackup records the three Internet Settings values. The *Existed flags
// distinguish "value was absent" from "value was empty", so Restore can delete
// versus set and reproduce the original state exactly.
type windowsBackup struct {
	ProxyEnable          uint32 `json:"proxyEnable"`
	ProxyEnableExisted   bool   `json:"proxyEnableExisted"`
	ProxyServer          string `json:"proxyServer"`
	ProxyServerExisted   bool   `json:"proxyServerExisted"`
	ProxyOverride        string `json:"proxyOverride"`
	ProxyOverrideExisted bool   `json:"proxyOverrideExisted"`
}

type darwinBackup struct {
	Services []darwinService `json:"services"`
}

type darwinService struct {
	Name          string   `json:"name"`
	WebEnabled    bool     `json:"webEnabled"`
	WebServer     string   `json:"webServer"`
	WebPort       int      `json:"webPort"`
	SecureEnabled bool     `json:"secureEnabled"`
	SecureServer  string   `json:"secureServer"`
	SecurePort    int      `json:"securePort"`
	Bypass        []string `json:"bypass"`
}

type linuxBackup struct {
	Mode        string `json:"mode"`
	HTTPHost    string `json:"httpHost"`
	HTTPPort    int    `json:"httpPort"`
	HTTPSHost   string `json:"httpsHost"`
	HTTPSPort   int    `json:"httpsPort"`
	IgnoreHosts string `json:"ignoreHosts"` // raw GVariant literal as gsettings returns it
}

// backupPath is <UserConfigDir>/psdns/sysproxy-backup.json (outside the repo, in
// the user's config dir, so it persists across runs for crash recovery).
func backupPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "psdns", "sysproxy-backup.json"), nil
}

func backupExists() bool {
	p, err := backupPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// writeBackup writes b atomically (temp file + rename) with 0600 perms.
func writeBackup(b Backup) error {
	p, err := backupPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// readBackup loads the backup file; ok is false when none exists. A corrupt
// backup is deleted and reported as absent so it cannot wedge startup.
func readBackup() (b Backup, ok bool, err error) {
	p, err := backupPath()
	if err != nil {
		return Backup{}, false, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Backup{}, false, nil
		}
		return Backup{}, false, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		_ = deleteBackup()
		return Backup{}, false, err
	}
	return b, true, nil
}

func deleteBackup() error {
	p, err := backupPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
