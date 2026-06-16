// Package selfupdate checks GitHub Releases for a newer psdns and (in Apply)
// replaces the running GUI binary in place, so users never have to download new
// versions by hand. Release metadata, version comparison, and checksum
// verification are done with the standard library plus golang.org/x/mod/semver;
// only the delicate "replace the running executable" step is delegated to
// github.com/minio/selfupdate (see apply.go).
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// Build-time identity. Version is injected via
// -ldflags "-X github.com/vitus9988/psdns/internal/selfupdate.Version=v1.2.3".
// The rest have working defaults so only Version must be set at build time.
var (
	Version     = "dev"             // current build version ("dev" for local builds)
	Repo        = "vitus9988/psdns" // owner/name on GitHub
	AssetPrefix = "psdns"           // release asset name prefix
	BinaryName  = "psdns-gui"       // binary base name extracted from the archive
)

const defaultAPIBase = "https://api.github.com"

// ErrRateLimited is returned when the GitHub API rejects the request, typically
// the unauthenticated 60 req/h/IP limit.
var ErrRateLimited = errors.New("selfupdate: GitHub API rate limited, try again later")

// CheckResult is the outcome of a version check. It is returned to the UI as-is,
// so its fields are the UI's data model.
type CheckResult struct {
	Current    string `json:"current"`    // this build's version
	Latest     string `json:"latest"`     // newest release tag, e.g. "v1.2.0"
	Newer      bool   `json:"newer"`      // a usable newer release exists
	ReleaseURL string `json:"releaseUrl"` // human release page (fallback link)
	AssetName  string `json:"assetName"`  // archive this OS/ARCH should download
	Available  bool   `json:"available"`  // AssetName is actually present in the release
}

// CanApply reports whether Apply can run: a strictly newer release whose
// matching asset exists. Dev / non-semver current builds never qualify.
func (r CheckResult) CanApply() bool { return r.Newer && r.Available }

type release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Checker queries the GitHub Releases API and caches the result briefly so
// repeated UI calls don't burn through the rate limit.
type Checker struct {
	HTTP    *http.Client
	APIBase string        // defaults to https://api.github.com (overridable in tests)
	TTL     time.Duration // cache lifetime; defaults to 10m

	mu       sync.Mutex
	cache    *CheckResult
	cachedAt time.Time
}

// NewChecker returns a Checker. A nil client gets a sane default.
func NewChecker(httpc *http.Client) *Checker {
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Checker{HTTP: httpc, APIBase: defaultAPIBase, TTL: 10 * time.Minute}
}

// Check returns the latest-release comparison. Unless force is set, a result
// cached within TTL is returned without a network call.
func (c *Checker) Check(ctx context.Context, force bool) (CheckResult, error) {
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	c.mu.Lock()
	if !force && c.cache != nil && time.Since(c.cachedAt) < ttl {
		r := *c.cache
		c.mu.Unlock()
		return r, nil
	}
	c.mu.Unlock()

	rel, err := c.fetchLatest(ctx)
	if err != nil {
		return CheckResult{Current: Version}, err
	}
	res := buildResult(rel)

	c.mu.Lock()
	c.cache = &res
	c.cachedAt = time.Now()
	c.mu.Unlock()
	return res, nil
}

func (c *Checker) fetchLatest(ctx context.Context) (release, error) {
	base := c.APIBase
	if base == "" {
		base = defaultAPIBase
	}
	url := fmt.Sprintf("%s/repos/%s/releases/latest", base, Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return release{}, err
	}
	// GitHub requires a User-Agent; without it the API returns 403.
	req.Header.Set("User-Agent", "psdns-gui/"+Version)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return release{}, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		return release{}, ErrRateLimited
	case resp.StatusCode != http.StatusOK:
		return release{}, fmt.Errorf("selfupdate: GitHub releases status %d", resp.StatusCode)
	}

	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return release{}, fmt.Errorf("selfupdate: decode release: %w", err)
	}
	return rel, nil
}

func buildResult(rel release) CheckResult {
	name := assetNameFor(runtime.GOOS, runtime.GOARCH, rel.TagName)
	return CheckResult{
		Current:    Version,
		Latest:     rel.TagName,
		Newer:      isNewer(rel.TagName, Version),
		ReleaseURL: rel.HTMLURL,
		AssetName:  name,
		Available:  findAsset(rel, name) != nil,
	}
}

// assetNameFor builds the archive name for a target, matching scripts/
// build-release.sh: psdns_<tag>_<os>_<arch>.(tar.gz|zip).
func assetNameFor(goos, goarch, tag string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s.%s", AssetPrefix, tag, goos, goarch, ext)
}

// checksumsNameFor builds the checksums file name: psdns_<tag>_checksums.txt.
func checksumsNameFor(tag string) string {
	return fmt.Sprintf("%s_%s_checksums.txt", AssetPrefix, tag)
}

func findAsset(rel release, name string) *asset {
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			return &rel.Assets[i]
		}
	}
	return nil
}

// isNewer reports whether latestTag is a strictly newer semver than currentVer.
// A non-semver current build (e.g. "dev" or a `git describe` SHA) is treated as
// "unknown", so it never reports an available update — matching the policy that
// only release-tag builds participate in auto-update.
func isNewer(latestTag, currentVer string) bool {
	lv := normalizeSemver(latestTag)
	cv := normalizeSemver(currentVer)
	if !semver.IsValid(lv) || !semver.IsValid(cv) {
		return false
	}
	return semver.Compare(lv, cv) > 0
}

// normalizeSemver trims and ensures a leading "v" so semver.IsValid accepts it.
func normalizeSemver(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "v") && !strings.HasPrefix(s, "V") {
		s = "v" + s
	}
	return s
}
