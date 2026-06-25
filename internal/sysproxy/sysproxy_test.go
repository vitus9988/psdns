package sysproxy

import (
	"encoding/json"
	"reflect"
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
