package sysproxy

import (
	"encoding/json"
	"net"
	"reflect"
	"strconv"
	"testing"
)

func TestFromAddr(t *testing.T) {
	tests := []struct {
		addr     string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{"127.0.0.1:8080", "127.0.0.1", 8080, false},
		{"127.0.0.1:8081", "127.0.0.1", 8081, false}, // fallback port
		{":8080", "127.0.0.1", 8080, false},          // empty host -> loopback
		{"0.0.0.0:8080", "127.0.0.1", 8080, false},   // wildcard -> dialable loopback
		{"[::]:8080", "::1", 8080, false},            // IPv6 wildcard -> IPv6 loopback
		{"[::1]:8080", "::1", 8080, false},
		{"bad", "", 0, true},
		{"127.0.0.1:0", "", 0, true},
		{"127.0.0.1:99999", "", 0, true},
	}
	for _, tc := range tests {
		got, err := FromAddr(tc.addr, nil)
		if tc.wantErr {
			if err == nil {
				t.Errorf("FromAddr(%q) expected error", tc.addr)
			}
			continue
		}
		if err != nil {
			t.Errorf("FromAddr(%q): %v", tc.addr, err)
			continue
		}
		if got.Host != tc.wantHost || got.Port != tc.wantPort {
			t.Errorf("FromAddr(%q) = %s:%d, want %s:%d", tc.addr, got.Host, got.Port, tc.wantHost, tc.wantPort)
		}
	}
}

func TestFormatProxyServer(t *testing.T) {
	got := formatProxyServer("127.0.0.1", 8080)
	want := "http=127.0.0.1:8080;https=127.0.0.1:8080"
	if got != want {
		t.Errorf("formatProxyServer = %q, want %q", got, want)
	}
	got = formatProxyServer("::1", 8080)
	want = "http=[::1]:8080;https=[::1]:8080"
	if got != want {
		t.Errorf("formatProxyServer(IPv6) = %q, want %q", got, want)
	}
}

func TestFormatProxyOverride(t *testing.T) {
	if got := formatProxyOverride([]string{"localhost", "127.0.0.1"}); got != "localhost;127.0.0.1;<local>" {
		t.Errorf("formatProxyOverride = %q", got)
	}
	if got := formatProxyOverride(nil); got != "<local>" {
		t.Errorf("formatProxyOverride(nil) = %q, want <local>", got)
	}
}

func TestFormatIgnoreHosts(t *testing.T) {
	if got := formatIgnoreHosts([]string{"localhost", "127.0.0.1"}); got != "['localhost','127.0.0.1']" {
		t.Errorf("formatIgnoreHosts = %q", got)
	}
	if got := formatIgnoreHosts(nil); got != "[]" {
		t.Errorf("formatIgnoreHosts(nil) = %q, want []", got)
	}
}

func TestParseNetworkServices(t *testing.T) {
	in := "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\n*Bluetooth PAN\nThunderbolt Bridge\n"
	got := parseNetworkServices([]byte(in))
	want := []string{"Wi-Fi", "Thunderbolt Bridge"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseNetworkServices = %v, want %v", got, want)
	}
}

func TestParseGetWebProxy(t *testing.T) {
	on := parseGetWebProxy([]byte("Enabled: Yes\nServer: 10.0.0.1\nPort: 3128\nAuthenticated Proxy Enabled: 0\n"))
	if !on.Enabled || on.Server != "10.0.0.1" || on.Port != 3128 {
		t.Errorf("parseGetWebProxy(on) = %+v", on)
	}
	off := parseGetWebProxy([]byte("Enabled: No\nServer:\nPort: 0\n"))
	if off.Enabled || off.Server != "" || off.Port != 0 {
		t.Errorf("parseGetWebProxy(off) = %+v", off)
	}
}

func TestParseProxyBypassDomains(t *testing.T) {
	got := parseProxyBypassDomains([]byte("*.local\n169.254/16\n"))
	if !reflect.DeepEqual(got, []string{"*.local", "169.254/16"}) {
		t.Errorf("parseProxyBypassDomains = %v", got)
	}
	if got := parseProxyBypassDomains([]byte("There aren't any bypass domains set on Wi-Fi.\n")); got != nil {
		t.Errorf("parseProxyBypassDomains(none) = %v, want nil", got)
	}
}

func TestParseGsettingsValue(t *testing.T) {
	if got := parseGsettingsValue([]byte("'manual'\n")); got != "manual" {
		t.Errorf("parseGsettingsValue = %q, want manual", got)
	}
	if got := parseGsettingsValue([]byte("8080\n")); got != "8080" {
		t.Errorf("parseGsettingsValue(int) = %q, want 8080", got)
	}
}

func TestParseWinINETProxyServer(t *testing.T) {
	tests := []struct {
		in       string
		wantHost string
		wantPort int
		wantOK   bool
	}{
		{"http=127.0.0.1:8080;https=127.0.0.1:8080", "127.0.0.1", 8080, true},
		{"http=127.0.0.1:8080;https=127.0.0.1:9090", "127.0.0.1", 9090, true}, // https preferred
		{"http=127.0.0.1:8080", "127.0.0.1", 8080, true},                      // only http present
		{"127.0.0.1:1080", "127.0.0.1", 1080, true},                           // bare form (all protocols)
		{"[::1]:8080", "::1", 8080, true},                                     // bare IPv6
		{"ftp=127.0.0.1:2121", "127.0.0.1", 2121, true},                       // unknown proto -> first parsable
		{"", "", 0, false},
		{"garbage", "", 0, false},
		{"https=127.0.0.1:0", "", 0, false}, // invalid port
	}
	for _, tc := range tests {
		h, p, ok := parseWinINETProxyServer(tc.in)
		if ok != tc.wantOK || h != tc.wantHost || p != tc.wantPort {
			t.Errorf("parseWinINETProxyServer(%q) = %q,%d,%v; want %q,%d,%v",
				tc.in, h, p, ok, tc.wantHost, tc.wantPort, tc.wantOK)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for _, h := range []string{"localhost", "LocalHost", "127.0.0.1", "127.0.0.5", "::1"} {
		if !isLoopback(h) {
			t.Errorf("isLoopback(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"10.0.0.1", "192.168.1.2", "example.com", ""} {
		if isLoopback(h) {
			t.Errorf("isLoopback(%q) = true, want false", h)
		}
	}
}

func TestCurrentFromBackup(t *testing.T) {
	tests := []struct {
		name string
		b    Backup
		want DetectedProxy
	}{
		{"empty", Backup{}, DetectedProxy{}},
		{"windows disabled", Backup{Windows: &windowsBackup{ProxyEnable: 0, ProxyServer: "http=127.0.0.1:8080;https=127.0.0.1:8080"}}, DetectedProxy{}},
		{"windows enabled", Backup{Windows: &windowsBackup{ProxyEnable: 1, ProxyServer: "http=127.0.0.1:8080;https=127.0.0.1:8080"}}, DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 8080}},
		{"windows enabled bad server", Backup{Windows: &windowsBackup{ProxyEnable: 1, ProxyServer: ""}}, DetectedProxy{}},
		{"darwin secure preferred", Backup{Darwin: &darwinBackup{Services: []darwinService{
			{Name: "Wi-Fi", WebEnabled: true, WebServer: "127.0.0.1", WebPort: 7000, SecureEnabled: true, SecureServer: "127.0.0.1", SecurePort: 8080},
		}}}, DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 8080}},
		{"darwin web only", Backup{Darwin: &darwinBackup{Services: []darwinService{
			{Name: "Wi-Fi", WebEnabled: true, WebServer: "127.0.0.1", WebPort: 7000},
		}}}, DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 7000}},
		{"darwin none enabled", Backup{Darwin: &darwinBackup{Services: []darwinService{
			{Name: "Wi-Fi"},
		}}}, DetectedProxy{}},
		{"linux manual https", Backup{Linux: &linuxBackup{Mode: "manual", HTTPHost: "127.0.0.1", HTTPPort: 7000, HTTPSHost: "127.0.0.1", HTTPSPort: 8080}}, DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 8080}},
		{"linux manual http only", Backup{Linux: &linuxBackup{Mode: "manual", HTTPHost: "127.0.0.1", HTTPPort: 7000}}, DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 7000}},
		{"linux mode none", Backup{Linux: &linuxBackup{Mode: "none", HTTPSHost: "127.0.0.1", HTTPSPort: 8080}}, DetectedProxy{}},
	}
	for _, tc := range tests {
		if got := currentFromBackup(tc.b); got != tc.want {
			t.Errorf("currentFromBackup(%s) = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestDetectedProxyConflictsWith(t *testing.T) {
	ours := struct {
		host string
		port int
	}{"127.0.0.1", 8080}
	tests := []struct {
		name string
		d    DetectedProxy
		want bool
	}{
		{"disabled", DetectedProxy{Enabled: false, Host: "127.0.0.1", Port: 9999}, false},
		{"non-loopback", DetectedProxy{Enabled: true, Host: "10.0.0.1", Port: 3128}, false},
		{"our own address", DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 8080}, false},
		{"other loopback port", DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 9090}, true},
		// Loopback spellings are one identity: only the port distinguishes another
		// proxy from our own (possibly leftover) entry.
		{"IPv6 loopback same port", DetectedProxy{Enabled: true, Host: "::1", Port: 8080}, false},
		{"localhost name same port", DetectedProxy{Enabled: true, Host: "localhost", Port: 8080}, false},
		{"IPv6 loopback other port", DetectedProxy{Enabled: true, Host: "::1", Port: 9090}, true},
		{"localhost name other port", DetectedProxy{Enabled: true, Host: "localhost", Port: 9090}, true},
	}
	for _, tc := range tests {
		if got := tc.d.ConflictsWith(ours.host, ours.port); got != tc.want {
			t.Errorf("%s: ConflictsWith = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A loopback entry never belongs to a non-loopback caller, even on the same port.
	if !(DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 8080}).ConflictsWith("192.168.1.5", 8080) {
		t.Error("loopback proxy vs non-loopback caller on the same port must conflict")
	}
}

func TestAlive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	if !Alive(host, port, ProbeTimeout) {
		t.Error("Alive = false with a live listener")
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if Alive(host, port, ProbeTimeout) {
		t.Error("Alive = true after the listener closed")
	}
}

func TestMatchesApplied(t *testing.T) {
	tests := []struct {
		name    string
		cur     DetectedProxy
		applied string
		want    bool
	}{
		{"exact", DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 8080}, "127.0.0.1:8080", true},
		{"loopback alias localhost", DetectedProxy{Enabled: true, Host: "localhost", Port: 8080}, "127.0.0.1:8080", true},
		{"loopback alias ipv6", DetectedProxy{Enabled: true, Host: "::1", Port: 8080}, "127.0.0.1:8080", true},
		{"other port", DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 3128}, "127.0.0.1:8080", false},
		{"disabled", DetectedProxy{Enabled: false, Host: "127.0.0.1", Port: 8080}, "127.0.0.1:8080", false},
		{"non-loopback case-insensitive", DetectedProxy{Enabled: true, Host: "MyHost.local", Port: 8080}, "myhost.local:8080", true},
		{"non-loopback mismatch", DetectedProxy{Enabled: true, Host: "10.0.0.1", Port: 8080}, "127.0.0.1:8080", false},
		{"unparsable applied", DetectedProxy{Enabled: true, Host: "127.0.0.1", Port: 8080}, "garbage", false},
	}
	for _, tc := range tests {
		if got := matchesApplied(tc.cur, tc.applied); got != tc.want {
			t.Errorf("%s: matchesApplied = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// aliveNever/aliveAlways are fake probes for neutralizeStale tests; aliveNone
// asserts the probe is not consulted at all.
func aliveNever(string, int) bool  { return false }
func aliveAlways(string, int) bool { return true }

func TestNeutralizeStale(t *testing.T) {
	ours := Settings{Host: "127.0.0.1", Port: 8080}

	t.Run("windows own address disabled without probe", func(t *testing.T) {
		in := Backup{Windows: &windowsBackup{
			ProxyEnable: 1, ProxyEnableExisted: true,
			ProxyServer: "http=127.0.0.1:8080;https=127.0.0.1:8080", ProxyServerExisted: true,
		}}
		got := neutralizeStale(in, ours, func(string, int) bool {
			t.Fatal("probe must not run for our own address")
			return true
		})
		if got.Windows.ProxyEnable != 0 {
			t.Error("own-address entry must be disabled")
		}
		if !got.Windows.ProxyEnableExisted || !got.Windows.ProxyServerExisted {
			t.Error("Existed flags must be preserved")
		}
		if in.Windows.ProxyEnable != 1 {
			t.Error("input backup must not be mutated")
		}
	})

	t.Run("windows dead loopback disabled", func(t *testing.T) {
		in := Backup{Windows: &windowsBackup{ProxyEnable: 1, ProxyServer: "https=127.0.0.1:3128"}}
		if got := neutralizeStale(in, ours, aliveNever); got.Windows.ProxyEnable != 0 {
			t.Error("dead loopback entry must be disabled")
		}
	})

	t.Run("windows live loopback kept", func(t *testing.T) {
		in := Backup{Windows: &windowsBackup{ProxyEnable: 1, ProxyServer: "https=127.0.0.1:3128"}}
		if got := neutralizeStale(in, ours, aliveAlways); got.Windows.ProxyEnable != 1 {
			t.Error("live third-party loopback proxy must be kept as captured")
		}
	})

	t.Run("windows non-loopback kept without probe", func(t *testing.T) {
		in := Backup{Windows: &windowsBackup{ProxyEnable: 1, ProxyServer: "https=10.0.0.1:3128"}}
		got := neutralizeStale(in, ours, func(string, int) bool {
			t.Fatal("non-loopback entries must never be probed")
			return false
		})
		if got.Windows.ProxyEnable != 1 {
			t.Error("corporate (non-loopback) proxy must be kept as captured")
		}
	})

	t.Run("windows disabled untouched", func(t *testing.T) {
		in := Backup{Windows: &windowsBackup{ProxyEnable: 0, ProxyServer: "https=127.0.0.1:8080"}}
		if got := neutralizeStale(in, ours, aliveNever); got.Windows.ProxyEnable != 0 {
			t.Error("disabled entry must stay disabled")
		}
	})

	t.Run("darwin per-entry", func(t *testing.T) {
		in := Backup{Darwin: &darwinBackup{Services: []darwinService{
			{
				Name:       "Wi-Fi",
				WebEnabled: true, WebServer: "127.0.0.1", WebPort: 8080, // ours -> off
				SecureEnabled: true, SecureServer: "127.0.0.1", SecurePort: 3128, // live other -> kept
				Bypass: []string{"*.local"},
			},
			{
				Name:       "Ethernet",
				WebEnabled: true, WebServer: "10.0.0.1", WebPort: 3128, // corporate -> kept
			},
		}}}
		got := neutralizeStale(in, ours, aliveAlways)
		wifi, eth := got.Darwin.Services[0], got.Darwin.Services[1]
		if wifi.WebEnabled {
			t.Error("Wi-Fi web entry (ours) must be disabled")
		}
		if !wifi.SecureEnabled {
			t.Error("Wi-Fi secure entry (live other proxy) must be kept")
		}
		if !reflect.DeepEqual(wifi.Bypass, []string{"*.local"}) {
			t.Error("Bypass must be preserved")
		}
		if !eth.WebEnabled {
			t.Error("Ethernet corporate entry must be kept")
		}
		if !in.Darwin.Services[0].WebEnabled {
			t.Error("input backup must not be mutated")
		}
	})

	t.Run("linux manual stale becomes none", func(t *testing.T) {
		in := Backup{Linux: &linuxBackup{Mode: "manual", HTTPSHost: "127.0.0.1", HTTPSPort: 8080}}
		if got := neutralizeStale(in, ours, aliveNever); got.Linux.Mode != "none" {
			t.Errorf("Mode = %q, want none", got.Linux.Mode)
		}
		if in.Linux.Mode != "manual" {
			t.Error("input backup must not be mutated")
		}
	})

	t.Run("linux manual live other kept", func(t *testing.T) {
		in := Backup{Linux: &linuxBackup{Mode: "manual", HTTPSHost: "127.0.0.1", HTTPSPort: 3128}}
		if got := neutralizeStale(in, ours, aliveAlways); got.Linux.Mode != "manual" {
			t.Errorf("Mode = %q, want manual (live third-party proxy)", got.Linux.Mode)
		}
	})

	t.Run("linux mode none untouched", func(t *testing.T) {
		in := Backup{Linux: &linuxBackup{Mode: "none", HTTPSHost: "127.0.0.1", HTTPSPort: 8080}}
		if got := neutralizeStale(in, ours, aliveNever); got.Linux.Mode != "none" {
			t.Errorf("Mode = %q, want none", got.Linux.Mode)
		}
	})
}

func TestApplyNeutralizesPoisonedCapture(t *testing.T) {
	redirectConfigDir(t)

	// A crashed run (with its backup lost) left the OS pointing at 127.0.0.1:8080;
	// this run bound 8080 again. The capture must not snapshot that leftover as
	// the "original" state, or Stop would restore a dead proxy and cut the network.
	origCapture, origApply := osCapture, osApply
	t.Cleanup(func() { osCapture, osApply = origCapture, origApply })
	var applied []Settings
	osCapture = func() (Backup, error) {
		return Backup{Windows: &windowsBackup{
			ProxyEnable: 1, ProxyEnableExisted: true,
			ProxyServer: "http=127.0.0.1:8080;https=127.0.0.1:8080", ProxyServerExisted: true,
		}}, nil
	}
	osApply = func(s Settings) error { applied = append(applied, s); return nil }

	s := Settings{Host: "127.0.0.1", Port: 8080}
	if err := Apply(s); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(applied) != 1 || applied[0].Port != 8080 {
		t.Fatalf("osApply calls = %+v, want one with port 8080", applied)
	}
	b, ok, err := readBackup()
	if err != nil || !ok {
		t.Fatalf("readBackup: ok=%v err=%v", ok, err)
	}
	if b.Windows == nil || b.Windows.ProxyEnable != 0 {
		t.Errorf("backup ProxyEnable = %+v, want 0 (poisoned entry neutralized)", b.Windows)
	}
	if b.AppliedProxy != "127.0.0.1:8080" {
		t.Errorf("AppliedProxy = %q", b.AppliedProxy)
	}
}

func TestBackupRoundTrip(t *testing.T) {
	b := Backup{
		Version:      backupVersion,
		OS:           "windows",
		AppliedProxy: "127.0.0.1:8080",
		Windows: &windowsBackup{
			ProxyEnable: 0, ProxyEnableExisted: true,
			ProxyServer: "", ProxyServerExisted: false,
			ProxyOverride: "<local>", ProxyOverrideExisted: true,
		},
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Backup
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(b, got) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, b)
	}
	if got.Windows.ProxyServerExisted {
		t.Error("ProxyServerExisted must survive as false (absent vs empty)")
	}
}
