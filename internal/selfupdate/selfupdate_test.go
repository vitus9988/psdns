package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.0", "v1.0.0", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.2.0", false},
		{"1.2.0", "1.0.0", true},   // missing leading v is normalized
		{"v1.2.0", "dev", false},   // dev current never updates
		{"dev", "v1.0.0", false},   // garbage latest ignored
		{"v1.2.0", "", false},      // empty current
		{"v1.2.0-rc1", "v1.1.0", true},
	}
	for _, c := range cases {
		if got := isNewer(c.latest, c.current); got != c.want {
			t.Errorf("isNewer(%q,%q)=%v want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestAssetNames(t *testing.T) {
	if got := assetNameFor("windows", "amd64", "v1.0.0"); got != "psdns_v1.0.0_windows_amd64.zip" {
		t.Errorf("windows asset = %q", got)
	}
	if got := assetNameFor("linux", "arm64", "v1.0.0"); got != "psdns_v1.0.0_linux_arm64.tar.gz" {
		t.Errorf("linux asset = %q", got)
	}
	if got := checksumsNameFor("v1.0.0"); got != "psdns_v1.0.0_checksums.txt" {
		t.Errorf("checksums = %q", got)
	}
}

// fakeReleaseServer serves a releases/latest payload and counts hits.
func fakeReleaseServer(t *testing.T, tag string, hits *int32) *httptest.Server {
	t.Helper()
	assetName := assetNameFor(runtime.GOOS, runtime.GOARCH, tag)
	rel := release{
		TagName: tag,
		HTMLURL: "https://github.com/vitus9988/psdns/releases/tag/" + tag,
		Assets: []asset{
			{Name: assetName, BrowserDownloadURL: "https://example.test/" + assetName},
			{Name: checksumsNameFor(tag), BrowserDownloadURL: "https://example.test/sums"},
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent header")
		}
		wantPath := "/repos/" + Repo + "/releases/latest"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q want %q", r.URL.Path, wantPath)
		}
		_ = json.NewEncoder(w).Encode(rel)
	}))
}

func TestCheckDetectsNewer(t *testing.T) {
	defer setVersion("v1.0.0")()
	var hits int32
	srv := fakeReleaseServer(t, "v1.2.0", &hits)
	defer srv.Close()

	c := NewChecker(srv.Client())
	c.APIBase = srv.URL
	res, err := c.Check(context.Background(), true)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Newer || !res.Available || !res.CanApply() {
		t.Fatalf("expected applicable update, got %+v", res)
	}
	if res.Latest != "v1.2.0" || res.Current != "v1.0.0" {
		t.Fatalf("versions: %+v", res)
	}
	if res.AssetName != assetNameFor(runtime.GOOS, runtime.GOARCH, "v1.2.0") {
		t.Fatalf("asset name: %q", res.AssetName)
	}
}

func TestCheckCaches(t *testing.T) {
	defer setVersion("v1.0.0")()
	var hits int32
	srv := fakeReleaseServer(t, "v1.2.0", &hits)
	defer srv.Close()

	c := NewChecker(srv.Client())
	c.APIBase = srv.URL
	c.TTL = time.Hour
	if _, err := c.Check(context.Background(), false); err != nil {
		t.Fatalf("Check#1: %v", err)
	}
	if _, err := c.Check(context.Background(), false); err != nil {
		t.Fatalf("Check#2: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 network hit (cached), got %d", got)
	}
	// force bypasses the cache
	if _, err := c.Check(context.Background(), true); err != nil {
		t.Fatalf("Check#3: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected 2 hits after force, got %d", got)
	}
}

func TestCheckRateLimited(t *testing.T) {
	defer setVersion("v1.0.0")()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewChecker(srv.Client())
	c.APIBase = srv.URL
	if _, err := c.Check(context.Background(), true); err != ErrRateLimited {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}

func TestCheckSameVersionNotNewer(t *testing.T) {
	defer setVersion("v1.2.0")()
	var hits int32
	srv := fakeReleaseServer(t, "v1.2.0", &hits)
	defer srv.Close()

	c := NewChecker(srv.Client())
	c.APIBase = srv.URL
	res, err := c.Check(context.Background(), true)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Newer || res.CanApply() {
		t.Fatalf("same version should not be newer: %+v", res)
	}
}

// setVersion temporarily overrides the build version, returning a restore func.
func setVersion(v string) func() {
	prev := Version
	Version = v
	return func() { Version = prev }
}
