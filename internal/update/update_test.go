package update

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"0.1.12", "0.1.13", true},
		{"0.1.13", "0.1.13", false},
		{"0.1.14", "0.1.13", false},
		{"0.2.0", "0.1.99", false},
		{"1.0.0", "1.0.1", true},
	}

	for _, tt := range tests {
		t.Run(tt.current+"_vs_"+tt.latest, func(t *testing.T) {
			got := IsNewer(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestRollbackFailsWithoutPrev(t *testing.T) {
	// Use a temp dir so no prev binary exists.
	tmp := t.TempDir()
	origPrev := prevBinaryPathFunc
	origBin := binaryPathFunc
	origRollback := rollbackAttemptedPathFunc
	t.Cleanup(func() {
		prevBinaryPathFunc = origPrev
		binaryPathFunc = origBin
		rollbackAttemptedPathFunc = origRollback
	})
	prevBinaryPathFunc = func() string { return filepath.Join(tmp, "gsd-cloud.prev") }
	binaryPathFunc = func() string { return filepath.Join(tmp, "gsd-cloud") }
	rollbackAttemptedPathFunc = func() string { return filepath.Join(tmp, "rollback-attempted") }

	err := Rollback()
	if err == nil {
		t.Fatal("expected error when prev binary does not exist")
	}
}

func TestVersionFromTag(t *testing.T) {
	got := VersionFromTag("daemon/v0.1.13")
	want := "0.1.13"
	if got != want {
		t.Errorf("VersionFromTag(\"daemon/v0.1.13\") = %q, want %q", got, want)
	}
}

func TestFetchLatestUsesLatestReleaseEndpoint(t *testing.T) {
	origClient := httpClient
	t.Cleanup(func() { httpClient = origClient })

	var requested []string
	httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requested = append(requested, req.URL.String())
		return jsonResponse(http.StatusOK, Release{TagName: "daemon/v0.2.37"}), nil
	})}

	release, err := FetchLatest()
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if release.TagName != "daemon/v0.2.37" {
		t.Fatalf("TagName = %q, want daemon/v0.2.37", release.TagName)
	}
	if len(requested) != 1 || requested[0] != latestReleaseURL {
		t.Fatalf("requested URLs = %#v, want only %s", requested, latestReleaseURL)
	}
}

func TestFetchLatestFallbackChoosesHighestDaemonSemver(t *testing.T) {
	origClient := httpClient
	t.Cleanup(func() { httpClient = origClient })

	httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case latestReleaseURL:
			return jsonResponse(http.StatusNotFound, map[string]string{"message": "not found"}), nil
		case releasesURL + "?per_page=20":
			return jsonResponse(http.StatusOK, []Release{
				{TagName: "daemon/v0.2.35"},
				{TagName: "daemon/v0.2.37"},
				{TagName: "other/v9.9.9"},
				{TagName: "daemon/v0.2.36"},
			}), nil
		default:
			t.Fatalf("unexpected URL %s", req.URL.String())
			return nil, nil
		}
	})}

	release, err := FetchLatest()
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if release.TagName != "daemon/v0.2.37" {
		t.Fatalf("TagName = %q, want daemon/v0.2.37", release.TagName)
	}
}

func TestAssetName(t *testing.T) {
	name := AssetName("v0.2.1")
	if name == "" {
		t.Fatal("AssetName() returned empty string")
	}
	if !contains(name, runtime.GOOS) {
		t.Errorf("AssetName() = %q, does not contain %q", name, runtime.GOOS)
	}
	if !contains(name, "v0.2.1") {
		t.Errorf("AssetName() = %q, does not contain version", name)
	}
}

func TestPiExtensionAssetName(t *testing.T) {
	name := PiExtensionAssetName("v0.2.1")
	want := "gsd-cloud-pi-extension-v0.2.1.tar.gz"
	if name != want {
		t.Fatalf("PiExtensionAssetName() = %q, want %q", name, want)
	}
}

func TestBackupAndRollback(t *testing.T) {
	tmp := t.TempDir()

	binPath := filepath.Join(tmp, "gsd-cloud")
	prevPath := filepath.Join(tmp, "gsd-cloud.prev")
	flagPath := filepath.Join(tmp, "rollback-attempted")

	// Create a fake binary.
	if err := os.WriteFile(binPath, []byte("v1-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	origPrev := prevBinaryPathFunc
	origBin := binaryPathFunc
	origRollback := rollbackAttemptedPathFunc
	t.Cleanup(func() {
		prevBinaryPathFunc = origPrev
		binaryPathFunc = origBin
		rollbackAttemptedPathFunc = origRollback
	})
	prevBinaryPathFunc = func() string { return prevPath }
	binaryPathFunc = func() string { return binPath }
	rollbackAttemptedPathFunc = func() string { return flagPath }

	// BackupCurrent should copy binary to prev.
	if err := BackupCurrent(); err != nil {
		t.Fatalf("BackupCurrent() error: %v", err)
	}

	data, err := os.ReadFile(prevPath)
	if err != nil {
		t.Fatalf("reading prev binary: %v", err)
	}
	if string(data) != "v1-binary" {
		t.Errorf("prev binary content = %q, want %q", data, "v1-binary")
	}

	// Overwrite current binary to simulate an update.
	if err := os.WriteFile(binPath, []byte("v2-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	// Rollback should restore v1.
	if err := Rollback(); err != nil {
		t.Fatalf("Rollback() error: %v", err)
	}

	data, err = os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("reading binary after rollback: %v", err)
	}
	if string(data) != "v1-binary" {
		t.Errorf("binary content after rollback = %q, want %q", data, "v1-binary")
	}

	// Second rollback should fail (flag set).
	if err := Rollback(); err == nil {
		t.Fatal("expected error on second rollback (flag should prevent infinite loops)")
	}

	// ClearRollbackFlag should allow rollback again.
	ClearRollbackFlag()

	// Need prev again for another rollback.
	if err := os.WriteFile(prevPath, []byte("v1-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(); err != nil {
		t.Fatalf("Rollback() after ClearRollbackFlag() error: %v", err)
	}
}

// TestEnsureExtensionHealthy_NoExtensionInstalled covers the fresh-machine
// case: no extension dir at all → no-op, no error. Pi-routed tasks will
// still fail later, but that's not the self-heal path's job.
func TestEnsureExtensionHealthy_NoExtensionInstalled(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureExtensionHealthy(dir); err != nil {
		t.Fatalf("expected nil for missing extension, got %v", err)
	}
}

// TestEnsureExtensionHealthy_AlreadyHealthy covers the steady-state case:
// extension is installed with a platform-native claude SDK binary present.
// Should be a no-op (no npm invocation).
func TestEnsureExtensionHealthy_AlreadyHealthy(t *testing.T) {
	dir := t.TempDir()
	// Stub a healthy extension layout: package.json + node_modules with a
	// platform-specific claude SDK binary.
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	platDir := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-agent-sdk-"+runtime.GOOS+"-"+runtime.GOARCH)
	if err := os.MkdirAll(platDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platDir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureExtensionHealthy(dir); err != nil {
		t.Fatalf("expected nil for healthy extension, got %v", err)
	}
}

// TestExtensionHasNativeBinary_RejectsAggregatePackage ensures we don't
// accept the non-platform-specific @anthropic-ai/claude-agent-sdk dir as
// proof of health — that dir exists in node_modules but doesn't carry the
// platform-native binary.
func TestExtensionHasNativeBinary_RejectsAggregatePackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-agent-sdk"), 0755); err != nil {
		t.Fatal(err)
	}
	if extensionHasNativeBinary(dir) {
		t.Fatal("expected false when only the aggregate package is present")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, payload any) *http.Response {
	raw, _ := json.Marshal(payload)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(raw)),
		Header:     make(http.Header),
	}
}
