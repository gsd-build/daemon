// Package update queries GitHub Releases for new daemon versions and handles
// downloading, checksum verification, backup, and rollback of the daemon binary.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gsd-build/daemon/internal/service"
)

const (
	releasesURL = "https://api.github.com/repos/gsd-build/daemon/releases"
	tagPrefix   = "daemon/v"
)

// Path function vars — overridden in tests for isolation.
var (
	binaryPathFunc            = service.BinaryPath
	prevBinaryPathFunc        = service.PrevBinaryPath
	rollbackAttemptedPathFunc = service.RollbackAttemptedPath
)

// Release represents a GitHub release.
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset represents a downloadable file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// parseSemver parses a version string like "1.2.3" (with optional "v" prefix)
// into three integer components.
func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	var result [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		result[i] = n
	}
	return result
}

// IsNewer returns true if latest is a higher semver than current.
// Both strings should be bare versions without a "v" prefix (e.g. "0.1.13").
func IsNewer(current, latest string) bool {
	c := parseSemver(current)
	l := parseSemver(latest)
	for i := 0; i < 3; i++ {
		if l[i] > c[i] {
			return true
		}
		if l[i] < c[i] {
			return false
		}
	}
	return false
}

// FetchLatest queries the GitHub Releases API and returns the first release
// whose tag starts with "daemon/v".
func FetchLatest() (*Release, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL+"?per_page=20", nil)
	if err != nil {
		return nil, fmt.Errorf("update: create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: GitHub API returned %d", resp.StatusCode)
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("update: decode releases: %w", err)
	}

	for i := range releases {
		if strings.HasPrefix(releases[i].TagName, tagPrefix) {
			return &releases[i], nil
		}
	}
	return nil, fmt.Errorf("update: no release found with tag prefix %q", tagPrefix)
}

// VersionFromTag strips the "daemon/v" prefix from a release tag.
func VersionFromTag(tag string) string {
	return strings.TrimPrefix(tag, tagPrefix)
}

// AssetName returns the expected binary asset name for the current platform,
// e.g. "gsd-cloud-darwin-arm64".
func AssetName() string {
	return fmt.Sprintf("gsd-cloud-%s-%s", runtime.GOOS, runtime.GOARCH)
}

// Download fetches the matching asset from the release, verifies its SHA256
// checksum against a SHA256SUMS asset (if present), and writes it to destPath
// with an atomic rename.
func Download(release *Release, destPath string) error {
	assetName := AssetName()

	var assetURL string
	var sumsURL string
	for _, a := range release.Assets {
		if a.Name == assetName {
			assetURL = a.BrowserDownloadURL
		}
		if a.Name == "SHA256SUMS" {
			sumsURL = a.BrowserDownloadURL
		}
	}
	if assetURL == "" {
		return fmt.Errorf("update: asset %q not found in release %s", assetName, release.TagName)
	}

	client := &http.Client{Timeout: 5 * time.Minute}

	// Download to a temp file, computing SHA256 as we go.
	tmpPath := destPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("update: create temp file: %w", err)
	}
	defer func() {
		out.Close()
		os.Remove(tmpPath) // clean up on any error path
	}()

	resp, err := client.Get(assetURL)
	if err != nil {
		return fmt.Errorf("update: download asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update: download returned %d", resp.StatusCode)
	}

	hasher := sha256.New()
	w := io.MultiWriter(out, hasher)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("update: write asset: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("update: close temp file: %w", err)
	}

	actualSum := hex.EncodeToString(hasher.Sum(nil))

	// Verify checksum if SHA256SUMS asset is available.
	if sumsURL != "" {
		expected, err := fetchExpectedChecksum(client, sumsURL, assetName)
		if err != nil {
			return fmt.Errorf("update: checksum verification: %w", err)
		}
		if actualSum != expected {
			return fmt.Errorf("update: checksum mismatch: got %s, want %s", actualSum, expected)
		}
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("update: chmod: %w", err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("update: atomic rename: %w", err)
	}

	return nil
}

// fetchExpectedChecksum downloads a SHA256SUMS file and returns the hex
// checksum for the named asset. The file format is "<hex>  <filename>" per line.
func fetchExpectedChecksum(client *http.Client, sumsURL, assetName string) (string, error) {
	resp, err := client.Get(sumsURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SHA256SUMS returned %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: "<hex>  <filename>" or "<hex> <filename>"
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == assetName {
			return parts[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("asset %q not found in SHA256SUMS", assetName)
}

// BackupCurrent copies the current binary to the prev path so it can be
// restored on rollback.
func BackupCurrent() error {
	src, err := os.Open(binaryPathFunc())
	if err != nil {
		return fmt.Errorf("update: open current binary: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(prevBinaryPathFunc())
	if err != nil {
		return fmt.Errorf("update: create prev binary: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("update: copy binary: %w", err)
	}

	if err := dst.Chmod(0755); err != nil {
		return fmt.Errorf("update: chmod prev binary: %w", err)
	}

	return nil
}

// Rollback replaces the current binary with the previous version. It uses a
// flag file to prevent infinite rollback loops — if a rollback has already been
// attempted, subsequent calls return an error until ClearRollbackFlag is called.
func Rollback() error {
	prevPath := prevBinaryPathFunc()
	binPath := binaryPathFunc()
	flagPath := rollbackAttemptedPathFunc()

	if _, err := os.Stat(flagPath); err == nil {
		return fmt.Errorf("update: rollback already attempted — clear flag first")
	}

	if _, err := os.Stat(prevPath); err != nil {
		return fmt.Errorf("update: no previous binary at %s: %w", prevPath, err)
	}

	// Set flag before attempting rollback.
	if err := os.WriteFile(flagPath, []byte("1"), 0644); err != nil {
		return fmt.Errorf("update: write rollback flag: %w", err)
	}

	if err := os.Rename(prevPath, binPath); err != nil {
		return fmt.Errorf("update: rename prev to current: %w", err)
	}

	return nil
}

// ClearRollbackFlag removes the rollback-attempted flag file, allowing future
// rollback attempts.
func ClearRollbackFlag() {
	os.Remove(rollbackAttemptedPathFunc())
}
