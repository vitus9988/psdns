package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"

	"github.com/vitus9988/psdns/internal/selfupdate"
)

// muteStdio silences fmt output on os.Stdout/os.Stderr for the test, so the
// user-facing Korean messages of runUpdate/usage don't pollute the test log.
func muteStdio(t *testing.T) {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = devnull, devnull
	t.Cleanup(func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		_ = devnull.Close()
	})
}

// muteLog discards the default logger's output for the test.
func muteLog(t *testing.T) {
	t.Helper()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
}

// setBuildVersion temporarily overrides the build version selfupdate compares
// against, so tests can simulate release and dev builds.
func setBuildVersion(t *testing.T, v string) {
	t.Helper()
	prev := selfupdate.Version
	selfupdate.Version = v
	t.Cleanup(func() { selfupdate.Version = prev })
}

// releaseAssetName mirrors selfupdate's asset naming (psdns_<tag>_<os>_<arch>)
// for the running platform, so fake payloads offer an asset Check will match.
func releaseAssetName(tag string) string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("psdns_%s_%s_%s.%s", tag, runtime.GOOS, runtime.GOARCH, ext)
}

// fakeReleaseAPI serves a GitHub releases/latest payload for tag and points the
// update code at it via the apiBase test seam. withAsset controls whether the
// payload contains this platform's archive.
func fakeReleaseAPI(t *testing.T, tag string, withAsset bool) {
	t.Helper()
	var assets []map[string]string
	if withAsset {
		assets = []map[string]string{
			{"name": releaseAssetName(tag), "browser_download_url": "https://example.test/" + releaseAssetName(tag)},
			{"name": "psdns_" + tag + "_checksums.txt", "browser_download_url": "https://example.test/sums"},
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag,
			"html_url": "https://github.com/vitus9988/psdns/releases/tag/" + tag,
			"assets":   assets,
		})
	}))
	prev := apiBase
	apiBase = srv.URL
	t.Cleanup(func() {
		apiBase = prev
		srv.Close()
	})
}

func TestRunUpdateCheckOnlyNewer(t *testing.T) {
	muteStdio(t)
	setBuildVersion(t, "v1.0.0")
	fakeReleaseAPI(t, "v9.9.9", true)
	// -check must report the newer version and return without exiting.
	runUpdate([]string{"-check"})
}

func TestRunUpdateAlreadyLatest(t *testing.T) {
	muteStdio(t)
	setBuildVersion(t, "v9.9.9")
	fakeReleaseAPI(t, "v1.0.0", true)
	runUpdate(nil)
}

func TestRunUpdateDevBuildNotEligible(t *testing.T) {
	muteStdio(t)
	setBuildVersion(t, "dev")
	fakeReleaseAPI(t, "v1.0.0", true)
	// A dev build never reports Newer, so this exercises the non-release hint.
	runUpdate(nil)
}

func TestRunUpdateAssetMissing(t *testing.T) {
	muteStdio(t)
	setBuildVersion(t, "v1.0.0")
	fakeReleaseAPI(t, "v9.9.9", false)
	// Newer but no archive for this platform: report and return, no replace.
	runUpdate(nil)
}

func TestNotifyUpdateLogsNewer(t *testing.T) {
	muteLog(t)
	setBuildVersion(t, "v1.0.0")
	fakeReleaseAPI(t, "v9.9.9", true)
	notifyUpdate()
}

func TestNotifyUpdateSilentOnError(t *testing.T) {
	muteLog(t)
	setBuildVersion(t, "v1.0.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	prev := apiBase
	apiBase = srv.URL
	t.Cleanup(func() {
		apiBase = prev
		srv.Close()
	})
	notifyUpdate()
}

func TestUpdateProgress(t *testing.T) {
	muteStdio(t)
	updateProgress(selfupdate.StageDownload, 0.5)
	updateProgress(selfupdate.StageDone, 1)
}
