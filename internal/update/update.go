// Package update queries GitHub Releases for daemon versions and handles
// downloading, checksum verification, pi extension installation, backup, and
// rollback of the daemon binary.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// AssetName returns the expected binary asset name for a given version and
// the current platform, e.g. "gsd-cloud-v0.2.1-darwin-arm64".
func AssetName(version string) string {
	return fmt.Sprintf("gsd-cloud-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
}

// PiExtensionAssetName returns the platform-independent pi extension archive
// name bundled with daemon releases.
func PiExtensionAssetName(version string) string {
	return fmt.Sprintf("gsd-cloud-pi-extension-%s.tar.gz", version)
}

// Download fetches the matching asset from the release, verifies its SHA256
// checksum against a signed SHA256SUMS asset, installs the bundled pi extension,
// and writes the binary to destPath with an atomic rename.
func Download(release *Release, destPath string) error {
	// Tag is "daemon/v0.2.1", asset name needs "v0.2.1"
	version := release.TagName
	if i := strings.LastIndex(version, "/"); i >= 0 {
		version = version[i+1:]
	}
	assetName := AssetName(version)
	extensionAssetName := PiExtensionAssetName(version)

	var assetURL string
	var extensionURL string
	var sumsURL string
	var sumsSigURL string
	for _, a := range release.Assets {
		if a.Name == assetName {
			assetURL = a.BrowserDownloadURL
		}
		if a.Name == extensionAssetName {
			extensionURL = a.BrowserDownloadURL
		}
		if a.Name == checksumAssetName {
			sumsURL = a.BrowserDownloadURL
		}
		if a.Name == checksumSignatureAssetName {
			sumsSigURL = a.BrowserDownloadURL
		}
	}
	if assetURL == "" {
		return fmt.Errorf("update: asset %q not found in release %s", assetName, release.TagName)
	}
	if extensionURL == "" {
		return fmt.Errorf("update: asset %q not found in release %s", extensionAssetName, release.TagName)
	}
	if sumsURL == "" || sumsSigURL == "" {
		return fmt.Errorf("update: signed checksums are required for release %s", release.TagName)
	}

	client := &http.Client{Timeout: 5 * time.Minute}

	tmpPath := destPath + ".tmp"
	defer func() {
		os.Remove(tmpPath) // clean up on any error path
	}()

	if err := downloadVerifiedAsset(client, assetURL, sumsURL, sumsSigURL, assetName, tmpPath, 0755); err != nil {
		return err
	}

	extensionArchivePath := destPath + ".pi-extension.tar.gz.tmp"
	defer os.Remove(extensionArchivePath)
	if err := downloadVerifiedAsset(client, extensionURL, sumsURL, sumsSigURL, extensionAssetName, extensionArchivePath, 0644); err != nil {
		return err
	}
	if err := installPiExtensionArchive(extensionArchivePath, filepath.Dir(destPath)); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("update: atomic rename: %w", err)
	}

	return nil
}

func downloadVerifiedAsset(client *http.Client, assetURL, sumsURL, sumsSigURL, assetName, destPath string, mode os.FileMode) error {
	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("update: create temp file: %w", err)
	}
	defer out.Close()

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

	expected, err := fetchVerifiedChecksum(client, sumsURL, sumsSigURL, assetName)
	if err != nil {
		return fmt.Errorf("update: checksum verification: %w", err)
	}
	if actualSum != expected {
		return fmt.Errorf("update: checksum mismatch for %s: got %s, want %s", assetName, actualSum, expected)
	}

	if err := os.Chmod(destPath, mode); err != nil {
		return fmt.Errorf("update: chmod: %w", err)
	}
	return nil
}

func installPiExtensionArchive(archivePath, installDir string) error {
	finalDir := filepath.Join(installDir, "pi-extension")
	tmpDir := filepath.Join(installDir, "pi-extension.tmp")
	prevDir := filepath.Join(installDir, "pi-extension.prev")

	if err := os.RemoveAll(tmpDir); err != nil {
		return fmt.Errorf("update: remove temp pi extension: %w", err)
	}
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return fmt.Errorf("update: create temp pi extension: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractTarGz(archivePath, tmpDir); err != nil {
		return err
	}

	// The release tarball ships source + package-lock.json only. Run npm to
	// resolve the right platform-native binaries on this machine; without
	// this the @anthropic-ai/claude-agent-sdk import fails and pi exits with
	// "Cannot find module '@anthropic-ai/claude-agent-sdk'" / "Unknown
	// provider 'claude-cli'". Mirrors install.sh's install_pi_extension.
	if err := installExtensionDependencies(tmpDir); err != nil {
		return err
	}

	if err := os.RemoveAll(prevDir); err != nil {
		return fmt.Errorf("update: remove previous pi extension backup: %w", err)
	}
	if _, err := os.Stat(finalDir); err == nil {
		if err := os.Rename(finalDir, prevDir); err != nil {
			return fmt.Errorf("update: backup pi extension: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("update: inspect pi extension: %w", err)
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		_ = os.Rename(prevDir, finalDir)
		return fmt.Errorf("update: install pi extension: %w", err)
	}
	if err := os.RemoveAll(prevDir); err != nil {
		return fmt.Errorf("update: remove previous pi extension backup: %w", err)
	}
	return nil
}

func installExtensionDependencies(extDir string) error {
	// Skip if the extracted tarball doesn't carry a package-lock.json. Older
	// release tarballs bundled node_modules directly; we shouldn't blow up
	// on those, and tests use minimal fixtures without a lockfile.
	if _, err := os.Stat(filepath.Join(extDir, "package-lock.json")); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("update: stat extension package-lock.json: %w", err)
	}
	npmPath, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("update: npm not found on PATH (required to install pi extension dependencies): %w", err)
	}
	cmd := exec.Command(npmPath, "ci", "--omit=dev", "--include=optional", "--no-audit", "--no-fund", "--silent")
	cmd.Dir = extDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("update: npm ci in %s failed: %w (%s)", extDir, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("update: open pi extension archive: %w", err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("update: read pi extension archive: %w", err)
	}
	defer gzr.Close()

	destClean, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("update: resolve pi extension path: %w", err)
	}

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("update: read pi extension entry: %w", err)
		}
		target, err := safeArchiveTarget(destClean, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("update: create pi extension dir: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("update: create pi extension parent: %w", err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("update: create pi extension file: %w", err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return fmt.Errorf("update: write pi extension file: %w", err)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("update: close pi extension file: %w", err)
			}
		case tar.TypeSymlink:
			targetLink, err := safeArchiveLinkTarget(destClean, target, hdr.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("update: create pi extension symlink parent: %w", err)
			}
			if err := os.Symlink(targetLink, target); err != nil {
				return fmt.Errorf("update: create pi extension symlink: %w", err)
			}
		default:
			return fmt.Errorf("update: unsupported pi extension archive entry %q", hdr.Name)
		}
	}
	return nil
}

func safeArchiveTarget(destDir, name string) (string, error) {
	clean := filepath.Clean(name)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
		return "", fmt.Errorf("update: unsafe pi extension archive entry %q", name)
	}
	target := filepath.Join(destDir, clean)
	targetClean, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("update: resolve pi extension entry: %w", err)
	}
	if targetClean != destDir && !strings.HasPrefix(targetClean, destDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("update: unsafe pi extension archive entry %q", name)
	}
	return targetClean, nil
}

func safeArchiveLinkTarget(destDir, targetPath, linkName string) (string, error) {
	if filepath.IsAbs(linkName) {
		return "", fmt.Errorf("update: unsafe pi extension symlink target %q", linkName)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(targetPath), linkName))
	resolvedAbs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("update: resolve pi extension symlink: %w", err)
	}
	if resolvedAbs != destDir && !strings.HasPrefix(resolvedAbs, destDir+string(os.PathSeparator)) {
		return "", fmt.Errorf("update: unsafe pi extension symlink target %q", linkName)
	}
	return linkName, nil
}

func fetchVerifiedChecksum(client *http.Client, sumsURL, signatureURL, assetName string) (string, error) {
	checksums, err := fetchAssetBytes(client, sumsURL, checksumAssetName)
	if err != nil {
		return "", err
	}
	signature, err := fetchAssetBytes(client, signatureURL, checksumSignatureAssetName)
	if err != nil {
		return "", err
	}
	if err := verifySignedChecksums(checksums, signature); err != nil {
		return "", err
	}
	return expectedChecksumFromSUMS(checksums, assetName)
}

func fetchAssetBytes(client *http.Client, assetURL, assetName string) ([]byte, error) {
	resp, err := client.Get(assetURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d", assetName, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func expectedChecksumFromSUMS(checksums []byte, assetName string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(checksums))
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
