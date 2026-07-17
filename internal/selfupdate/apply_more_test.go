package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// releaseJSONServer serves rel as the releases/latest payload.
func releaseJSONServer(t *testing.T, rel release) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(rel)
	}))
}

// TestApplyErrorPaths covers Apply's guard rails without ever reaching the
// executable replacement: a failing metadata fetch, an up-to-date release, and
// a release with no asset for this platform. The replace step itself is
// deliberately untested — it would swap the running test binary.
func TestApplyErrorPaths(t *testing.T) {
	defer setVersion("v1.0.0")()

	// Metadata fetch fails → the error is surfaced and StageStart was reported.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	c := NewChecker(bad.Client())
	c.APIBase = bad.URL
	var stages []Stage
	err := c.Apply(context.Background(), func(s Stage, _ float64) { stages = append(stages, s) })
	if err == nil {
		t.Fatal("Apply must fail when the release metadata fetch fails")
	}
	if len(stages) == 0 || stages[0] != StageStart {
		t.Fatalf("Apply must report StageStart first, got %v", stages)
	}

	// Latest is not newer → ErrUpToDate before any download.
	same := releaseJSONServer(t, release{TagName: "v1.0.0"})
	defer same.Close()
	c = NewChecker(same.Client())
	c.APIBase = same.URL
	if err := c.Apply(context.Background(), nil); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("want ErrUpToDate, got %v", err)
	}

	// Newer release without a matching asset → ErrNoAsset from the fetch step.
	noAsset := releaseJSONServer(t, release{TagName: "v9.9.9"})
	defer noAsset.Close()
	c = NewChecker(noAsset.Client())
	c.APIBase = noAsset.URL
	if err := c.Apply(context.Background(), nil); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("want ErrNoAsset, got %v", err)
	}
}

// TestGetRejectsNon200 covers get's status guard and its error propagation
// through downloadChecksums and downloadBytes.
func TestGetRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	c := NewChecker(srv.Client())
	if _, err := c.get(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "다운로드 실패") {
		t.Fatalf("want download-failed error, got %v", err)
	}
	if _, err := c.downloadChecksums(context.Background(), srv.URL); err == nil {
		t.Fatal("downloadChecksums must propagate the get error")
	}
	if _, err := c.downloadBytes(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("downloadBytes must propagate the get error")
	}
}

// TestGetTransportError covers get's Do-error branch.
func TestGetTransportError(t *testing.T) {
	c := NewChecker(&http.Client{Transport: roundTripErr{}})
	if _, err := c.get(context.Background(), "http://example.invalid/x"); err == nil {
		t.Fatal("get must surface a transport error")
	}
}

type roundTripErr struct{}

func (roundTripErr) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("transport down")
}

// TestDownloadBytesShortBody covers the mid-read error branch: the server
// advertises more bytes than it sends, so the client read fails.
func TestDownloadBytesShortBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()

	c := NewChecker(srv.Client())
	if _, err := c.downloadBytes(context.Background(), srv.URL, func(float64) {}); err == nil {
		t.Fatal("downloadBytes must fail on a truncated body")
	}
}

// TestFetchLatestErrorPaths covers fetchLatest's failure branches: an invalid
// request URL, a dead connection, an unexpected status, and a garbled body.
func TestFetchLatestErrorPaths(t *testing.T) {
	// Control character in the URL → request construction fails.
	c := NewChecker(nil)
	c.APIBase = "http://example.invalid/\x01"
	if _, err := c.fetchLatest(context.Background()); err == nil {
		t.Fatal("want an error for an invalid API base URL")
	}

	// Connection refused → Do fails.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	c = NewChecker(nil)
	c.APIBase = deadURL
	if _, err := c.fetchLatest(context.Background()); err == nil {
		t.Fatal("want an error for a dead server")
	}

	// Unexpected (non-rate-limit) status.
	boom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer boom.Close()
	c = NewChecker(boom.Client())
	c.APIBase = boom.URL
	if _, err := c.fetchLatest(context.Background()); err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("want status-500 error, got %v", err)
	}

	// Body is not JSON → decode error.
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer garbled.Close()
	c = NewChecker(garbled.Client())
	c.APIBase = garbled.URL
	if _, err := c.fetchLatest(context.Background()); err == nil || !strings.Contains(err.Error(), "decode release") {
		t.Fatalf("want decode error, got %v", err)
	}
}

// TestCheckZeroTTLDefaults covers Check's TTL fallback for a zero-value Checker
// (constructed directly, not via NewChecker).
func TestCheckZeroTTLDefaults(t *testing.T) {
	defer setVersion("v1.0.0")()
	var hits int32
	srv := fakeReleaseServer(t, "v1.2.0", &hits)
	defer srv.Close()

	c := &Checker{HTTP: srv.Client(), APIBase: srv.URL} // TTL zero → 10m default
	if _, err := c.Check(context.Background(), false); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if _, err := c.Check(context.Background(), false); err != nil {
		t.Fatalf("Check#2: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("zero TTL must fall back to caching, got %d hits", got)
	}
}

// TestExtractFromTarGzErrors covers the tar.gz failure branches: not gzip at
// all, gzip wrapping a broken tar stream, and an archive without the binary.
func TestExtractFromTarGzErrors(t *testing.T) {
	if _, err := extractFromTarGz([]byte("not-gzip"), "psdns-gui"); err == nil {
		t.Fatal("want a gzip header error")
	}

	var broken bytes.Buffer
	gw := gzip.NewWriter(&broken)
	_, _ = gw.Write([]byte("this is not a tar stream, but long enough to look like one....."))
	_ = gw.Close()
	if _, err := extractFromTarGz(broken.Bytes(), "psdns-gui"); err == nil {
		t.Fatal("want a tar parse error")
	}

	var missing bytes.Buffer
	gw = gzip.NewWriter(&missing)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "pkg/README.md", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gw.Close()
	if _, err := extractFromTarGz(missing.Bytes(), "psdns-gui"); !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("want ErrBinaryNotFound, got %v", err)
	}
}

// TestExtractFromZipInvalid covers the zip-reader error branch.
func TestExtractFromZipInvalid(t *testing.T) {
	if _, err := extractFromZip([]byte("not-a-zip"), "psdns-gui.exe"); err == nil {
		t.Fatal("want a zip format error")
	}
}
