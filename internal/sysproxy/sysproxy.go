// Package sysproxy points the OS-wide web proxy (http + https) at the local
// psdns proxy and restores the previous setting on stop. It is used only by the
// GUI, but is deliberately cgo-free so the CGO_ENABLED=0 CLI could import it too:
// macOS/Linux shell out to networksetup/gsettings, Windows uses the registry plus
// a wininet.dll call loaded dynamically. Each OS provides apply/restore/capture/
// supported in a build-tagged file; this file holds the public API and the
// snapshot/restore bookkeeping shared across them.
package sysproxy

import (
	"fmt"
	"net"
	"runtime"
	"strconv"
)

// Settings is the proxy configuration to apply. Host/Port come from the live
// HTTP proxy listener — the supervisor's actual bound address, which may differ
// from the configured port after a fallback — so callers pass that, never a
// hardcoded value.
type Settings struct {
	Host   string
	Port   int
	Bypass []string // hosts/CIDRs that must NOT go through the proxy
}

// FromAddr builds Settings from a "host:port" address (e.g. a supervisor
// Listener.Addr) and a bypass list. An empty host defaults to loopback.
func FromAddr(addr string, bypass []string) (Settings, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return Settings{}, fmt.Errorf("sysproxy: bad address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return Settings{}, fmt.Errorf("sysproxy: bad port in %q", addr)
	}
	host = proxyHostForDial(host)
	return Settings{Host: host, Port: port, Bypass: bypass}, nil
}

func proxyHostForDial(host string) string {
	if host == "" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			return "127.0.0.1"
		}
		return "::1"
	}
	return host
}

// DefaultBypass lists the hosts that must bypass the proxy: loopback and private
// ranges, so local and LAN traffic (and the proxy reaching itself) is never
// routed through psdns.
func DefaultBypass() []string {
	return []string{"localhost", "127.0.0.1", "::1", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
}

// Supported reports whether system-proxy automation is available on this OS and
// environment (e.g. a graphical session with gsettings on Linux).
func Supported() bool { return supported() }

// Apply points the OS web proxy at s. The first call (no live backup on disk)
// snapshots the current OS proxy state so Restore can put it back; a pre-existing
// backup is preserved, because it means we already applied and the snapshot must
// not be overwritten with our own values.
//
// On error the OS may be left partially modified and the backup has already been
// written, so the caller MUST still arrange for Restore to run (e.g. on stop): a
// failed Apply does not mean the OS was left untouched.
func Apply(s Settings) error {
	if !backupExists() {
		b, err := capture()
		if err != nil {
			return err
		}
		b.Version = backupVersion
		b.OS = runtime.GOOS
		b.AppliedProxy = net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
		if err := writeBackup(b); err != nil {
			return err
		}
	}
	return apply(s)
}

// Restore puts the OS proxy back to the snapshot taken by Apply and removes the
// backup. It is a no-op when there is no backup, so it is safe to call
// unconditionally and more than once.
func Restore() error {
	_, err := restoreBackup()
	return err
}

// RecoverStale restores a backup left by a previous run that exited without
// restoring (a crash or force-kill). On a clean start there is no backup and it
// does nothing. Returns whether a stale backup was actually recovered.
func RecoverStale() (bool, error) {
	return restoreBackup()
}

// restoreBackup is the shared core of Restore and RecoverStale: read the backup,
// hand it to the OS restore, then delete it. A backup written on a different OS
// (e.g. a synced config dir) is discarded rather than applied.
func restoreBackup() (bool, error) {
	b, ok, err := readBackup()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if b.OS != runtime.GOOS {
		return false, deleteBackup()
	}
	if err := restore(b); err != nil {
		return false, err
	}
	return true, deleteBackup()
}
