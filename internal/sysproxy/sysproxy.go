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
	"time"
)

// capture/apply/restore are provided per-OS in build-tagged files and shell out
// to OS tools (networksetup, gsettings, the registry). These indirections let
// tests swap them out and exercise Apply/Restore/RecoverStale hermetically on
// any OS; production code must always go through them.
var (
	osCapture = capture
	osApply   = apply
	osRestore = restore
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

// DetectedProxy is the web proxy the OS is configured to use right now, extracted
// from a read-only snapshot. Enabled is true only when the OS currently routes
// web traffic through Host:Port.
type DetectedProxy struct {
	Enabled bool
	Host    string
	Port    int
}

// Current returns the web proxy the OS is set to use at this moment. It makes no
// changes (it only reads via capture), so it is safe to call before Apply to warn
// about a conflicting local filtering proxy (e.g. AdGuard) that Apply would
// otherwise overwrite.
func Current() (DetectedProxy, error) {
	b, err := osCapture()
	if err != nil {
		return DetectedProxy{}, err
	}
	return currentFromBackup(b), nil
}

// ConflictsWith reports whether d is a *different* loopback proxy than host:port —
// i.e. another local filtering proxy that Apply would route around. A disabled
// proxy, a non-loopback proxy, or our own address is not a conflict. All loopback
// spellings (localhost, 127.0.0.1, ::1) count as one identity: after a port
// fallback the OS may hold our own leftover entry under a different spelling, so
// for a loopback caller only the port can tell another proxy from our own.
func (d DetectedProxy) ConflictsWith(host string, port int) bool {
	if !d.Enabled || !isLoopback(d.Host) {
		return false
	}
	if d.Port != port {
		return true
	}
	return !isLoopback(host)
}

// ProbeTimeout bounds Alive's connect attempt. A loopback connect or refusal
// settles in microseconds; the margin only covers a heavily loaded machine.
const ProbeTimeout = 300 * time.Millisecond

// Alive reports whether something is accepting TCP connections at host:port. The
// GUI uses it to tell a live conflicting proxy (never overwrite it) from the dead
// leftover of a crashed run (safe to take over). It is a plain userspace dial —
// for a "localhost" host Go tries each resolved loopback address within timeout.
func Alive(host string, port int, timeout time.Duration) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

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
		b, err := osCapture()
		if err != nil {
			return err
		}
		// Never snapshot a state that only "worked" because of us: a leftover
		// entry pointing at our own address, or at a dead loopback proxy, is
		// flipped to disabled so a later Restore cannot cut the network.
		b = neutralizeStale(b, s, func(host string, port int) bool { return Alive(host, port, ProbeTimeout) })
		b.Version = backupVersion
		b.OS = runtime.GOOS
		b.AppliedProxy = net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
		if err := writeBackup(b); err != nil {
			return err
		}
	}
	return osApply(s)
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
// does nothing. Unlike Restore — the in-session stop path, which always owes its
// restore — arbitrary time has passed since the crash: when the OS proxy no
// longer matches what that run applied (the user or another app changed it in
// the meantime), restoring the old snapshot would clobber that change, so the
// backup is only dropped. A detection error falls open to restoring, matching
// the conflict guard's stance; a legacy backup without AppliedProxy restores as
// before. Returns whether a stale backup was actually recovered.
func RecoverStale() (bool, error) {
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
	if b.AppliedProxy != "" {
		if cur, cerr := Current(); cerr == nil && !matchesApplied(cur, b.AppliedProxy) {
			return false, deleteBackup()
		}
	}
	if err := osRestore(b); err != nil {
		return false, err
	}
	return true, deleteBackup()
}

// restoreBackup is the core of Restore: read the backup, hand it to the OS
// restore, then delete it. A backup written on a different OS (e.g. a synced
// config dir) is discarded rather than applied.
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
	if err := osRestore(b); err != nil {
		return false, err
	}
	return true, deleteBackup()
}
