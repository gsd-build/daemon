package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type BrowserUpdateRequest struct {
	Command        string
	Version        string
	CurrentVersion string
	Source         string
	Digest         string
	SignatureOK    bool
	ApprovalToken  string
	Rollback       bool
}

type BrowserUpdateRunner interface {
	RunBrowserUpdate(ctx context.Context, req BrowserUpdateRequest) error
}

type BrowserUpdater struct {
	Runner BrowserUpdateRunner
	Fetch  func(ctx context.Context, source string) ([]byte, error)
}

func (u BrowserUpdater) Run(ctx context.Context, req BrowserUpdateRequest) error {
	if strings.TrimSpace(req.Command) != "" {
		return errors.New("cloud-provided commands are not allowed")
	}
	if req.Version == "" {
		return errors.New("version is required")
	}
	if req.Source == "" {
		return errors.New("source is required")
	}
	if req.Digest == "" {
		return errors.New("digest is required")
	}
	if !allowedBrowserReleaseURL(req.Source) {
		return errors.New("source URL is not allowlisted")
	}
	if !req.SignatureOK {
		return errors.New("valid signature is required")
	}
	if req.CurrentVersion != "" && compareSemver(strings.TrimPrefix(req.Version, "v"), strings.TrimPrefix(req.CurrentVersion, "v")) < 0 && !req.Rollback {
		return errors.New("downgrade requires rollback approval")
	}
	if req.Rollback && req.ApprovalToken == "" {
		return errors.New("rollback approval token is required")
	}
	if u.Fetch != nil {
		bytes, err := u.Fetch(ctx, req.Source)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(bytes)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimPrefix(req.Digest, "sha256:")) {
			return errors.New("digest mismatch")
		}
	}
	if u.Runner == nil {
		return nil
	}
	return u.Runner.RunBrowserUpdate(ctx, req)
}

func allowedBrowserReleaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" {
		return false
	}
	return strings.HasPrefix(parsed.Path, "/gsd-build/browser/releases/download/")
}

func RedactBrowserUpdateLog(value string) string {
	redacted := value
	if strings.Contains(redacted, "token=") {
		redacted = strings.ReplaceAll(redacted, "token=", "token=[redacted]")
	}
	if strings.Contains(redacted, "approval_") {
		redacted = strings.ReplaceAll(redacted, "approval_", "approval_[redacted]_")
	}
	if strings.HasPrefix(redacted, "/") {
		return "[path]"
	}
	return redacted
}

func browserDigest(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(sum[:]))
}
