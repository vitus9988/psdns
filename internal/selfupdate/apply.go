package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"runtime"
	"strings"

	minio "github.com/minio/selfupdate"
)

// Stage marks progress through an Apply run, reported via the progress callback.
type Stage string

const (
	StageStart    Stage = "start"
	StageDownload Stage = "download"
	StageVerify   Stage = "verify"
	StageExtract  Stage = "extract"
	StageReplace  Stage = "replace"
	StageDone     Stage = "done"
)

var (
	ErrNoAsset          = errors.New("selfupdate: 이 플랫폼에 맞는 릴리즈 파일이 없어요")
	ErrNoChecksums      = errors.New("selfupdate: 릴리즈에 checksums 파일이 없어요")
	ErrChecksumMismatch = errors.New("selfupdate: 내려받은 파일 검증에 실패했어요 (체크섬 불일치)")
	ErrBinaryNotFound   = errors.New("selfupdate: 아카이브 안에서 실행파일을 찾지 못했어요")
	ErrUpToDate         = errors.New("selfupdate: 이미 최신 버전이에요")
)

// maxDownload caps in-memory archive size; the published archives are a few MB.
const maxDownload = 200 << 20

// Apply downloads the newest release for this OS/ARCH, verifies it against the
// published checksums, extracts the GUI binary, and atomically replaces the
// running executable. The caller should restart afterwards. progress may be nil.
func (c *Checker) Apply(ctx context.Context, progress func(Stage, float64)) error {
	report := func(s Stage, p float64) {
		if progress != nil {
			progress(s, p)
		}
	}
	report(StageStart, 0)

	rel, err := c.fetchLatest(ctx)
	if err != nil {
		return err
	}
	if !isNewer(rel.TagName, Version) {
		return ErrUpToDate
	}

	bin, err := c.fetchVerifiedBinary(ctx, rel, report)
	if err != nil {
		return err
	}

	report(StageReplace, 0.9)
	if err := minio.Apply(bytes.NewReader(bin), minio.Options{}); err != nil {
		if rb := minio.RollbackError(err); rb != nil {
			return fmt.Errorf("selfupdate: 교체에 실패했고 되돌리기도 실패했어요. 릴리즈 페이지에서 직접 받아 주세요: %v", rb)
		}
		return fmt.Errorf("selfupdate: 실행파일 교체에 실패했어요: %w", err)
	}
	report(StageDone, 1)
	return nil
}

// fetchVerifiedBinary resolves the asset for this platform, downloads it and the
// checksums file, verifies the archive's SHA-256, and returns the extracted GUI
// binary bytes. It deliberately does NOT touch the filesystem, so it is fully
// unit-testable without replacing the real executable.
func (c *Checker) fetchVerifiedBinary(ctx context.Context, rel release, report func(Stage, float64)) ([]byte, error) {
	assetName := assetNameFor(runtime.GOOS, runtime.GOARCH, rel.TagName)
	a := findAsset(rel, assetName)
	if a == nil {
		return nil, ErrNoAsset
	}
	sumsAsset := findAsset(rel, checksumsNameFor(rel.TagName))
	if sumsAsset == nil {
		return nil, ErrNoChecksums
	}

	report(StageDownload, 0.05)
	sums, err := c.downloadChecksums(ctx, sumsAsset.BrowserDownloadURL)
	if err != nil {
		return nil, err
	}
	want, ok := sums[assetName]
	if !ok {
		return nil, fmt.Errorf("selfupdate: checksums에 %s 항목이 없어요", assetName)
	}

	archive, err := c.downloadBytes(ctx, a.BrowserDownloadURL, func(p float64) {
		report(StageDownload, 0.05+0.6*p)
	})
	if err != nil {
		return nil, err
	}

	report(StageVerify, 0.7)
	got := sha256.Sum256(archive)
	if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return nil, ErrChecksumMismatch
	}

	report(StageExtract, 0.8)
	binName := c.Binary
	if binName == "" {
		binName = BinaryName
	}
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	return extractBinary(archive, assetName, binName)
}

func (c *Checker) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "psdns-gui/"+Version)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("selfupdate: 다운로드 실패 (status %d)", resp.StatusCode)
	}
	return resp, nil
}

func (c *Checker) downloadChecksums(ctx context.Context, url string) (map[string]string, error) {
	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	m := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		fields := strings.Fields(strings.TrimSpace(sc.Text()))
		if len(fields) != 2 {
			continue
		}
		// `shasum` may prefix the name with '*' for binary mode.
		name := strings.TrimPrefix(fields[1], "*")
		m[path.Base(name)] = fields[0]
	}
	return m, sc.Err()
}

func (c *Checker) downloadBytes(ctx context.Context, url string, onProgress func(float64)) ([]byte, error) {
	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	total := resp.ContentLength
	// Reject an over-cap archive up front with a clear message. Without this the
	// LimitReader below would silently truncate at maxDownload and the truncated
	// bytes would fail the SHA-256 check, surfacing as a confusing "checksum
	// mismatch" instead of "too large".
	if total > maxDownload {
		return nil, fmt.Errorf("selfupdate: 릴리즈 파일이 너무 커요 (%d바이트 > 최대 %d바이트)", total, int64(maxDownload))
	}
	var buf bytes.Buffer
	body := io.LimitReader(resp.Body, maxDownload)
	chunk := make([]byte, 64*1024)
	var read int64
	for {
		n, rerr := body.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			read += int64(n)
			if total > 0 && onProgress != nil {
				onProgress(float64(read) / float64(total))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	return buf.Bytes(), nil
}

// extractBinary pulls the GUI binary out of the archive by matching its base
// name (so it works whether the binary is top-level, as on Linux/Windows, or
// nested inside psdns-gui.app/Contents/MacOS on macOS). Matching the base name
// in-memory also sidesteps zip-slip path-traversal concerns.
func extractBinary(archive []byte, assetName, binName string) ([]byte, error) {
	if strings.HasSuffix(assetName, ".zip") {
		return extractFromZip(archive, binName)
	}
	return extractFromTarGz(archive, binName)
}

func extractFromTarGz(data []byte, binName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == binName {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
	return nil, ErrBinaryNotFound
}

func extractFromZip(data []byte, binName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxDownload))
		rc.Close()
		return b, err
	}
	return nil, ErrBinaryNotFound
}
