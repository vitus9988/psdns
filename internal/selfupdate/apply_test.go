package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// buildArchive packs content as the GUI binary into the archive format this
// platform uses (zip on Windows, tar.gz elsewhere), mirroring the release layout.
func buildArchive(binName string, content []byte) []byte {
	if runtime.GOOS == "windows" {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("psdns_pkg/" + binName)
		_, _ = w.Write(content)
		_ = zw.Close()
		return buf.Bytes()
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{
		Name: "psdns_pkg/" + binName, Mode: 0o755,
		Size: int64(len(content)), Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write(content)
	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

func guiBinName() string {
	if runtime.GOOS == "windows" {
		return BinaryName + ".exe"
	}
	return BinaryName
}

func updateServer(t *testing.T, tag string, archive []byte, sumHex string) (*httptest.Server, release) {
	t.Helper()
	assetName := assetNameFor(runtime.GOOS, runtime.GOARCH, tag)
	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sumHex, assetName)
	})
	srv := httptest.NewServer(mux)
	rel := release{
		TagName: tag,
		Assets: []asset{
			{Name: assetName, BrowserDownloadURL: srv.URL + "/asset"},
			{Name: checksumsNameFor(tag), BrowserDownloadURL: srv.URL + "/sums"},
		},
	}
	return srv, rel
}

func TestFetchVerifiedBinary(t *testing.T) {
	defer setVersion("v1.0.0")()
	tag := "v1.2.0"
	content := []byte("NEW-PSDNS-GUI-BINARY-" + tag)
	archive := buildArchive(guiBinName(), content)
	sum := sha256.Sum256(archive)

	srv, rel := updateServer(t, tag, archive, hex.EncodeToString(sum[:]))
	defer srv.Close()

	c := NewChecker(srv.Client())
	bin, err := c.fetchVerifiedBinary(context.Background(), rel, func(Stage, float64) {})
	if err != nil {
		t.Fatalf("fetchVerifiedBinary: %v", err)
	}
	if !bytes.Equal(bin, content) {
		t.Fatalf("extracted binary mismatch: got %q", bin)
	}
}

// TestDownloadBytesRejectsOversized verifies an archive advertising a size over
// the cap is rejected up front with a clear "too large" error rather than being
// silently truncated (which would later surface as a confusing checksum mismatch).
func TestDownloadBytesRejectsOversized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Advertise a size past the cap but send nothing: downloadBytes must
		// reject on the declared Content-Length before reading the body.
		w.Header().Set("Content-Length", strconv.FormatInt(int64(maxDownload)+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(srv.Client())
	_, err := c.downloadBytes(context.Background(), srv.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "너무 커요") {
		t.Fatalf("want too-large error, got %v", err)
	}
}

// TestChecksumMismatchBlocksReplace is the critical security test: a wrong
// checksum must stop the update before any replacement.
func TestChecksumMismatchBlocksReplace(t *testing.T) {
	defer setVersion("v1.0.0")()
	tag := "v1.2.0"
	archive := buildArchive(guiBinName(), []byte("tampered"))
	wrong := hex.EncodeToString(make([]byte, 32)) // all-zero, never matches

	srv, rel := updateServer(t, tag, archive, wrong)
	defer srv.Close()

	c := NewChecker(srv.Client())
	_, err := c.fetchVerifiedBinary(context.Background(), rel, func(Stage, float64) {})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
}

func TestFetchVerifiedBinaryNoAsset(t *testing.T) {
	defer setVersion("v1.0.0")()
	rel := release{TagName: "v1.2.0", Assets: []asset{{Name: "psdns_v1.2.0_other_arch.tar.gz"}}}
	c := NewChecker(nil)
	_, err := c.fetchVerifiedBinary(context.Background(), rel, func(Stage, float64) {})
	if !errors.Is(err, ErrNoAsset) {
		t.Fatalf("want ErrNoAsset, got %v", err)
	}
}

func TestExtractBinaryFindsNestedEntry(t *testing.T) {
	// macOS layout: binary nested in the .app bundle. Base-name match must find it.
	content := []byte("mach-o")
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, name := range []string{
		"psdns_v1_darwin_arm64/psdns",                                  // the CLI — must be ignored
		"psdns_v1_darwin_arm64/psdns-gui.app/Contents/MacOS/psdns-gui", // the GUI
	} {
		body := []byte("cli")
		if name[len(name)-3:] == "gui" {
			body = content
		}
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	_ = gw.Close()

	got, err := extractFromTarGz(buf.Bytes(), "psdns-gui")
	if err != nil {
		t.Fatalf("extractFromTarGz: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

func TestExtractBinaryCLIIgnoresGUIBundle(t *testing.T) {
	// The macOS archive holds the CLI (psdns) at top level AND the GUI binary
	// nested in the .app bundle. Extracting "psdns" for the CLI must return the
	// top-level CLI, never the nested GUI binary.
	cli := []byte("cli-binary")
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range []struct {
		name string
		body []byte
	}{
		{"psdns_v1_darwin_arm64/psdns", cli},
		{"psdns_v1_darwin_arm64/psdns-gui.app/Contents/MacOS/psdns-gui", []byte("gui-binary")},
	} {
		_ = tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(e.body)
	}
	_ = tw.Close()
	_ = gw.Close()

	got, err := extractFromTarGz(buf.Bytes(), "psdns")
	if err != nil {
		t.Fatalf("extractFromTarGz: %v", err)
	}
	if !bytes.Equal(got, cli) {
		t.Fatalf("got %q, want CLI binary %q", got, cli)
	}
}

func TestFetchVerifiedBinaryCLIOverride(t *testing.T) {
	defer setVersion("v1.0.0")()
	tag := "v1.2.0"
	content := []byte("NEW-PSDNS-CLI-" + tag)
	cliName := "psdns"
	if runtime.GOOS == "windows" {
		cliName = "psdns.exe"
	}
	archive := buildArchive(cliName, content)
	sum := sha256.Sum256(archive)

	srv, rel := updateServer(t, tag, archive, hex.EncodeToString(sum[:]))
	defer srv.Close()

	c := NewChecker(srv.Client())
	c.Binary = "psdns" // CLI extracts "psdns", not the default "psdns-gui"
	bin, err := c.fetchVerifiedBinary(context.Background(), rel, func(Stage, float64) {})
	if err != nil {
		t.Fatalf("fetchVerifiedBinary: %v", err)
	}
	if !bytes.Equal(bin, content) {
		t.Fatalf("extracted CLI binary mismatch: got %q", bin)
	}
}

// TestExtractFromZipFindsNestedEntry covers the Windows (.zip) extraction path,
// including base-name matching past a nested directory and a decoy file.
func TestExtractFromZipFindsNestedEntry(t *testing.T) {
	content := []byte("windows-exe-bytes")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct {
		name string
		body []byte
	}{
		{"psdns_v1_windows_amd64/README.md", []byte("readme")},
		{"psdns_v1_windows_amd64/psdns-gui.exe", content},
	} {
		w, _ := zw.Create(e.name)
		_, _ = w.Write(e.body)
	}
	_ = zw.Close()

	got, err := extractFromZip(buf.Bytes(), "psdns-gui.exe")
	if err != nil {
		t.Fatalf("extractFromZip: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

func TestExtractFromZipNotFound(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("pkg/other.txt")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()

	if _, err := extractFromZip(buf.Bytes(), "psdns-gui.exe"); !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("want ErrBinaryNotFound, got %v", err)
	}
}

// TestExtractBinaryDispatchesByExtension covers both branches of extractBinary:
// a .zip asset routes to the zip reader, anything else to tar.gz.
func TestExtractBinaryDispatchesByExtension(t *testing.T) {
	content := []byte("payload-bytes")

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	zf, _ := zw.Create("pkg/bin")
	_, _ = zf.Write(content)
	_ = zw.Close()
	if got, err := extractBinary(zbuf.Bytes(), "psdns_v1_windows_amd64.zip", "bin"); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("zip dispatch: got %q err %v", got, err)
	}

	var tbuf bytes.Buffer
	gw := gzip.NewWriter(&tbuf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "pkg/bin", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(content)
	_ = tw.Close()
	_ = gw.Close()
	if got, err := extractBinary(tbuf.Bytes(), "psdns_v1_linux_amd64.tar.gz", "bin"); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("tar.gz dispatch: got %q err %v", got, err)
	}
}
