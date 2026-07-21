package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// macAppBundleRoot reports whether exe is the inner Mach-O of a macOS .app
// bundle (…/Name.app/Contents/MacOS/binary) and, if so, returns the bundle root
// (…/Name.app). This is pure path logic: on other platforms a real executable
// path never has this shape, so callers fall through to the plain in-place
// binary replacement.
func macAppBundleRoot(exe string) (root string, ok bool) {
	macOS := filepath.Dir(exe)      // …/Name.app/Contents/MacOS
	contents := filepath.Dir(macOS) // …/Name.app/Contents
	root = filepath.Dir(contents)   // …/Name.app
	if filepath.Base(macOS) == "MacOS" && filepath.Base(contents) == "Contents" && strings.HasSuffix(root, ".app") {
		return root, true
	}
	return "", false
}

// replaceMacAppBundle swaps the running .app bundle at bundleRoot with the one
// packaged in the (already SHA-256-verified) archive. Replacing the whole bundle
// rather than just the inner executable keeps the Mach-O, Info.plist and code
// signature consistent, so a signed/notarized bundle stays launchable after the
// update. The new bundle is staged as a sibling of the old one (same filesystem)
// so the final swap is an atomic rename.
func replaceMacAppBundle(archive []byte, bundleRoot string) error {
	parent := filepath.Dir(bundleRoot)
	staging, err := os.MkdirTemp(parent, ".psdns-update-*")
	if err != nil {
		return fmt.Errorf("selfupdate: 임시 폴더를 만들지 못했어요: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	newBundle := filepath.Join(staging, "bundle.app")
	if err := extractAppBundleTarGz(archive, newBundle); err != nil {
		return err
	}
	return swapDir(bundleRoot, newBundle)
}

// extractAppBundleTarGz extracts the top-level *.app subtree from a tar.gz
// archive into dest, so dest becomes the new bundle root (dest/Contents/…).
// Entries outside the .app (e.g. the sibling CLI binary) are ignored. Paths are
// checked to stay within dest (zip-slip guard) and file modes are preserved so
// the executable stays runnable.
func extractAppBundleTarGz(data []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rel, ok := insideAppBundle(h.Name)
		if !ok {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if !withinDir(dest, target) {
			return fmt.Errorf("selfupdate: 아카이브에 비정상 경로가 있어요: %q", h.Name)
		}
		found = true
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeBundleFile(tr, target, h.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		default:
			// Skip anything else (fifos, devices): an .app never contains them.
		}
	}
	if !found {
		return ErrBinaryNotFound
	}
	return nil
}

// insideAppBundle returns name's path relative to the top-level .app directory
// (e.g. "psdns.app/Contents/MacOS/psdns-gui" → "Contents/MacOS/psdns-gui"), or
// ("", false) when name is not inside a top-level .app (e.g. the sibling CLI
// binary at the archive root).
func insideAppBundle(name string) (string, bool) {
	name = strings.TrimPrefix(path.Clean(name), "./")
	first, rest, found := strings.Cut(name, "/")
	if !found || !strings.HasSuffix(first, ".app") {
		return "", false
	}
	return rest, true
}

// withinDir reports whether target lies inside dir (not equal to, and not
// escaping via "..").
func withinDir(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeBundleFile(r io.Reader, target string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(r, maxDownload))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// swapDir atomically replaces the directory oldPath with newPath (both must be
// on the same filesystem). The old directory is moved aside first so a failure
// to move the new one into place can be rolled back; the moved-aside copy is
// removed only once the swap succeeds.
func swapDir(oldPath, newPath string) error {
	backup := oldPath + ".psdns-old"
	_ = os.RemoveAll(backup)
	if err := os.Rename(oldPath, backup); err != nil {
		return fmt.Errorf("selfupdate: 기존 앱을 옮기지 못했어요: %w", err)
	}
	if err := os.Rename(newPath, oldPath); err != nil {
		_ = os.Rename(backup, oldPath) // roll back
		return fmt.Errorf("selfupdate: 새 앱을 설치하지 못했어요: %w", err)
	}
	_ = os.RemoveAll(backup)
	return nil
}
