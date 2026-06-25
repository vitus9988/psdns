//go:build linux

package sysproxy

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Linux targets GNOME via gsettings (per-user dconf, no admin rights). Non-GNOME
// desktops (KDE etc.) and headless sessions are unsupported: Supported() returns
// false when gsettings is missing or there is no graphical session.

const gnomeProxy = "org.gnome.system.proxy"

func supported() bool {
	if _, err := lookCmd("gsettings"); err != nil {
		return false
	}
	return hasGraphicalSession()
}

func capture() (Backup, error) {
	l := &linuxBackup{
		Mode:        gsettingsGet(gnomeProxy, "mode"),
		HTTPHost:    gsettingsGet(gnomeProxy+".http", "host"),
		HTTPPort:    atoiSafe(gsettingsGet(gnomeProxy+".http", "port")),
		HTTPSHost:   gsettingsGet(gnomeProxy+".https", "host"),
		HTTPSPort:   atoiSafe(gsettingsGet(gnomeProxy+".https", "port")),
		IgnoreHosts: gsettingsGetRaw(gnomeProxy, "ignore-hosts"),
	}
	return Backup{Linux: l}, nil
}

func apply(s Settings) error {
	port := strconv.Itoa(s.Port)
	cmds := [][]string{
		{"set", gnomeProxy, "mode", "manual"},
		{"set", gnomeProxy + ".http", "host", s.Host},
		{"set", gnomeProxy + ".http", "port", port},
		{"set", gnomeProxy + ".https", "host", s.Host},
		{"set", gnomeProxy + ".https", "port", port},
		{"set", gnomeProxy, "ignore-hosts", formatIgnoreHosts(s.Bypass)},
	}
	return runGsettings(cmds, "시스템 프록시 설정에 실패했어요")
}

func restore(b Backup) error {
	if b.Linux == nil {
		return nil
	}
	l := b.Linux
	mode := l.Mode
	if mode == "" {
		mode = "none"
	}
	cmds := [][]string{{"set", gnomeProxy, "mode", mode}}
	if l.HTTPHost != "" {
		cmds = append(cmds, []string{"set", gnomeProxy + ".http", "host", l.HTTPHost})
	}
	cmds = append(cmds, []string{"set", gnomeProxy + ".http", "port", strconv.Itoa(l.HTTPPort)})
	if l.HTTPSHost != "" {
		cmds = append(cmds, []string{"set", gnomeProxy + ".https", "host", l.HTTPSHost})
	}
	cmds = append(cmds, []string{"set", gnomeProxy + ".https", "port", strconv.Itoa(l.HTTPSPort)})
	if l.IgnoreHosts != "" {
		cmds = append(cmds, []string{"set", gnomeProxy, "ignore-hosts", l.IgnoreHosts})
	}
	return runGsettings(cmds, "시스템 프록시 원복에 실패했어요")
}

func runGsettings(cmds [][]string, failMsg string) error {
	var firstErr error
	for _, args := range cmds {
		if out, err := runCmd("gsettings", args...); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %v (%s)", failMsg, err, strings.TrimSpace(string(out)))
		}
	}
	return firstErr
}

func gsettingsGet(schema, key string) string {
	out, err := runCmd("gsettings", "get", schema, key)
	if err != nil {
		return ""
	}
	return parseGsettingsValue(out)
}

func gsettingsGetRaw(schema, key string) string {
	out, err := runCmd("gsettings", "get", schema, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func hasGraphicalSession() bool {
	for _, v := range []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS"} {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
