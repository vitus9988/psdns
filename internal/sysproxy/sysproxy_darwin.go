//go:build darwin

package sysproxy

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// macOS uses the networksetup(8) CLI. Changing proxy settings generally needs
// admin rights — even members of the admin group are often refused without sudo
// — so a non-interactive GUI run may fail; apply surfaces that as a friendly
// "needs admin" message and the user falls back to copying the proxy address.

func supported() bool {
	_, err := lookCmd("networksetup")
	return err == nil
}

func capture() (Backup, error) {
	services, err := listNetworkServices()
	if err != nil {
		return Backup{}, err
	}
	d := &darwinBackup{}
	for _, svc := range services {
		web := getWebProxy(svc, false)
		secure := getWebProxy(svc, true)
		d.Services = append(d.Services, darwinService{
			Name:          svc,
			WebEnabled:    web.Enabled,
			WebServer:     web.Server,
			WebPort:       web.Port,
			SecureEnabled: secure.Enabled,
			SecureServer:  secure.Server,
			SecurePort:    secure.Port,
			Bypass:        getProxyBypassDomains(svc),
		})
	}
	return Backup{Darwin: d}, nil
}

func apply(s Settings) error {
	services, err := listNetworkServices()
	if err != nil {
		return err
	}
	port := strconv.Itoa(s.Port)
	var firstErr error
	for _, svc := range services {
		// Stop at the first failure for a service: never enable the proxy state
		// (a later command in the sequence) on top of a server-set that failed,
		// which would route traffic to a stale/unset server.
		for _, args := range applyDarwinArgs(svc, s.Host, port, s.Bypass) {
			out, err := runCmd("networksetup", args...)
			if err != nil {
				if firstErr == nil {
					firstErr = classifyDarwinErr(err, out)
				}
				break
			}
		}
	}
	return firstErr
}

func restore(b Backup) error {
	if b.Darwin == nil {
		return nil
	}
	var firstErr error
	for _, svc := range b.Darwin.Services {
		for _, args := range restoreDarwinArgs(svc) {
			if out, err := runCmd("networksetup", args...); err != nil && firstErr == nil {
				firstErr = classifyDarwinErr(err, out)
			}
		}
	}
	return firstErr
}

// applyDarwinArgs is the pure list of networksetup invocations to point svc at
// host:port (web + secure web) and set the bypass domains.
func applyDarwinArgs(svc, host, port string, bypass []string) [][]string {
	cmds := [][]string{
		{"-setwebproxy", svc, host, port},
		{"-setsecurewebproxy", svc, host, port},
		{"-setwebproxystate", svc, "on"},
		{"-setsecurewebproxystate", svc, "on"},
	}
	if len(bypass) > 0 {
		cmds = append(cmds, append([]string{"-setproxybypassdomains", svc}, bypass...))
	}
	return cmds
}

// restoreDarwinArgs is the pure list of networksetup invocations to put svc back
// to its captured state (re-enable with the old server, or turn off).
func restoreDarwinArgs(svc darwinService) [][]string {
	var cmds [][]string
	// Require a valid port too: a captured Enabled state with port 0 (e.g. from
	// malformed -getwebproxy output) would make -setwebproxy fail, so fall back
	// to turning the proxy off rather than emitting a doomed command.
	if svc.WebEnabled && svc.WebServer != "" && svc.WebPort > 0 {
		cmds = append(cmds,
			[]string{"-setwebproxy", svc.Name, svc.WebServer, strconv.Itoa(svc.WebPort)},
			[]string{"-setwebproxystate", svc.Name, "on"},
		)
	} else {
		cmds = append(cmds, []string{"-setwebproxystate", svc.Name, "off"})
	}
	if svc.SecureEnabled && svc.SecureServer != "" && svc.SecurePort > 0 {
		cmds = append(cmds,
			[]string{"-setsecurewebproxy", svc.Name, svc.SecureServer, strconv.Itoa(svc.SecurePort)},
			[]string{"-setsecurewebproxystate", svc.Name, "on"},
		)
	} else {
		cmds = append(cmds, []string{"-setsecurewebproxystate", svc.Name, "off"})
	}
	if len(svc.Bypass) > 0 {
		cmds = append(cmds, append([]string{"-setproxybypassdomains", svc.Name}, svc.Bypass...))
	} else {
		cmds = append(cmds, []string{"-setproxybypassdomains", svc.Name, "Empty"})
	}
	return cmds
}

func listNetworkServices() ([]string, error) {
	out, err := runCmd("networksetup", "-listallnetworkservices")
	if err != nil {
		return nil, classifyDarwinErr(err, out)
	}
	return parseNetworkServices(out), nil
}

func getWebProxy(svc string, secure bool) webProxyState {
	flag := "-getwebproxy"
	if secure {
		flag = "-getsecurewebproxy"
	}
	out, err := runCmd("networksetup", flag, svc)
	if err != nil {
		return webProxyState{}
	}
	return parseGetWebProxy(out)
}

func getProxyBypassDomains(svc string) []string {
	out, err := runCmd("networksetup", "-getproxybypassdomains", svc)
	if err != nil {
		return nil
	}
	return parseProxyBypassDomains(out)
}

// classifyDarwinErr turns a networksetup failure into a friendly Korean message,
// flagging the common admin-rights case so the GUI can tell the user to fall
// back to copying the proxy address.
func classifyDarwinErr(err error, out []byte) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(string(out)), "admin") {
		return errors.New("시스템 프록시를 바꾸려면 관리자 권한이 필요해요. 프록시 주소를 복사해 브라우저에 직접 넣어 주세요")
	}
	return fmt.Errorf("시스템 프록시 설정에 실패했어요: %v (%s)", err, strings.TrimSpace(string(out)))
}
