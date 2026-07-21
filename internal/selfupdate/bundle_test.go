package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	minio "github.com/minio/selfupdate"
)

func TestMacAppBundleRoot(t *testing.T) {
	root, ok := macAppBundleRoot("/Applications/psdns.app/Contents/MacOS/psdns-gui")
	if !ok || root != "/Applications/psdns.app" {
		t.Fatalf("macAppBundleRoot = %q,%v; want /Applications/psdns.app,true", root, ok)
	}
	for _, p := range []string{
		"/usr/local/bin/psdns",              // bare CLI binary
		"/Applications/psdns.app/psdns-gui", // missing Contents/MacOS
		"/home/u/notanapp/Contents/MacOS/x", // parent dir is not *.app
	} {
		if r, ok := macAppBundleRoot(p); ok {
			t.Fatalf("macAppBundleRoot(%q) = %q,true; want _,false", p, r)
		}
	}
}

type tarEntry struct {
	name string
	body string
	mode int64  // 0 -> 0o644
	link string // non-empty -> a symlink to this target
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if e.link != "" {
			if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeSymlink, Linkname: e.link, Mode: 0o777}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(e.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustWriteDir(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestExtractAppBundleTarGz verifies the whole .app subtree is extracted (with
// executable bits preserved) and the sibling CLI binary at the archive root is
// ignored.
func TestExtractAppBundleTarGz(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{name: "psdns", body: "cli", mode: 0o755}, // sibling CLI — must be ignored
		{name: "psdns.app/Contents/Info.plist", body: "<plist/>"},
		{name: "psdns.app/Contents/MacOS/psdns-gui", body: "gui-bin", mode: 0o755},
		{name: "psdns.app/Contents/Resources/icon.icns", body: "icon"},
	})

	dest := filepath.Join(t.TempDir(), "out.app")
	if err := extractAppBundleTarGz(archive, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "psdns")); !os.IsNotExist(err) {
		t.Fatal("sibling CLI binary leaked into the bundle")
	}
	binPath := filepath.Join(dest, "Contents", "MacOS", "psdns-gui")
	if b, err := os.ReadFile(binPath); err != nil || string(b) != "gui-bin" {
		t.Fatalf("inner binary = %q, err %v; want gui-bin", b, err)
	}
	if fi, _ := os.Stat(binPath); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("inner binary not executable: mode %v", fi.Mode())
	}
	if b, err := os.ReadFile(filepath.Join(dest, "Contents", "Info.plist")); err != nil || string(b) != "<plist/>" {
		t.Fatalf("Info.plist = %q, err %v", b, err)
	}
}

// TestExtractAppBundleTarGzMissingApp verifies an archive with no .app subtree
// (e.g. an ARM CLI-only build) is reported rather than silently producing an
// empty bundle.
func TestExtractAppBundleTarGzMissingApp(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{{name: "psdns", body: "cli", mode: 0o755}})
	dest := filepath.Join(t.TempDir(), "out.app")
	if err := extractAppBundleTarGz(archive, dest); !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("want ErrBinaryNotFound, got %v", err)
	}
}

// TestSwapDir verifies the atomic directory swap replaces the old tree and
// leaves no staging or backup behind.
func TestSwapDir(t *testing.T) {
	base := t.TempDir()
	oldDir := filepath.Join(base, "cur")
	newDir := filepath.Join(base, "new")
	mustWriteDir(t, oldDir, "marker", "old")
	mustWriteDir(t, newDir, "marker", "new")

	if err := swapDir(oldDir, newDir); err != nil {
		t.Fatalf("swapDir: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(oldDir, "marker")); err != nil || string(b) != "new" {
		t.Fatalf("after swap marker = %q, err %v; want new", b, err)
	}
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Fatal("staging dir should have been renamed away")
	}
	if _, err := os.Stat(oldDir + ".psdns-old"); !os.IsNotExist(err) {
		t.Fatal("backup should have been removed after a successful swap")
	}
}

func swapExecutable(path string) func() {
	prev := osExecutable
	osExecutable = func() (string, error) { return path, nil }
	return func() { osExecutable = prev }
}

func swapMinio(fn func(io.Reader, minio.Options) error) func() {
	prev := minioApply
	minioApply = fn
	return func() { minioApply = prev }
}

// TestReplaceUpdatesMacAppBundle verifies that when the running executable is
// inside a .app bundle, replace swaps the whole bundle from the archive and does
// NOT fall back to the in-place inner-binary replacement.
func TestReplaceUpdatesMacAppBundle(t *testing.T) {
	bundleRoot := filepath.Join(t.TempDir(), "psdns.app")
	mustWriteDir(t, filepath.Join(bundleRoot, "Contents", "MacOS"), "psdns-gui", "OLD")
	exe := filepath.Join(bundleRoot, "Contents", "MacOS", "psdns-gui")

	archive := makeTarGz(t, []tarEntry{
		{name: "psdns", body: "cli", mode: 0o755},
		{name: "psdns.app/Contents/Info.plist", body: "<plist/>"},
		{name: "psdns.app/Contents/MacOS/psdns-gui", body: "NEW", mode: 0o755},
	})

	defer swapExecutable(exe)()
	minioCalled := false
	defer swapMinio(func(io.Reader, minio.Options) error { minioCalled = true; return nil })()

	c := &Checker{}
	if err := c.replace(archive, "psdns_v1_darwin_arm64.tar.gz"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if minioCalled {
		t.Fatal("bundle path must not fall back to the in-place binary replace")
	}
	if b, err := os.ReadFile(exe); err != nil || string(b) != "NEW" {
		t.Fatalf("inner binary after update = %q, err %v; want NEW", b, err)
	}
}

// TestReplaceUsesBinaryPathOutsideBundle verifies the plain in-place binary
// replacement is used when the executable is not inside a .app bundle (the CLI,
// or Linux/Windows).
func TestReplaceUsesBinaryPathOutsideBundle(t *testing.T) {
	c := &Checker{}
	archive := makeTarGz(t, []tarEntry{{name: c.binaryFileName(), body: "BIN", mode: 0o755}})

	defer swapExecutable(filepath.Join(t.TempDir(), "bin", c.binaryFileName()))()
	var got []byte
	defer swapMinio(func(r io.Reader, _ minio.Options) error {
		got, _ = io.ReadAll(r)
		return nil
	})()

	if err := c.replace(archive, "psdns_v1_linux_amd64.tar.gz"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if string(got) != "BIN" {
		t.Fatalf("in-place replace got %q, want BIN", got)
	}
}
