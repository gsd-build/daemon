package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var tinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

func TestImageUploadScopeRejectsOutOfScopeAbsolutePath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, tinyPNG, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	_, err := imageUploadFromReadPath(outside, root, nil)
	if err == nil {
		t.Fatal("expected out-of-scope path rejection")
	}
}

func TestImageUploadScopeRejectsRelativePathEscapingCWD(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	outside := filepath.Join(parent, "outside.png")
	if err := os.WriteFile(outside, tinyPNG, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	_, err := imageUploadFromReadPath("../outside.png", root, nil)
	if err == nil {
		t.Fatal("expected escaping relative path rejection")
	}
}

func TestImageUploadScopeRejectsSymlinkEscapingTaskRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, tinyPNG, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	link := filepath.Join(root, "link.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("cannot create symlink")
	}

	_, err := imageUploadFromReadPath(link, root, nil)
	if err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

func TestImageUploadScopeRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.png")
	data := append([]byte{}, tinyPNG...)
	data = append(data, make([]byte, maxImageUploadBytes+1-len(data))...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	_, err := imageUploadFromReadPath(path, root, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want oversized rejection", err)
	}
}

func TestImageUploadScopeRejectsExtensionSpoofing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fake.png")
	if err := os.WriteFile(path, []byte("plain text"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	_, err := imageUploadFromReadPath(path, root, nil)
	if err == nil || !strings.Contains(err.Error(), "content type") {
		t.Fatalf("error = %v, want content rejection", err)
	}
}

func TestImageUploadScopeRejectsSVG(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "vector.svg")
	if err := os.WriteFile(path, []byte("<svg></svg>"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	_, err := imageUploadFromReadPath(path, root, nil)
	if err == nil || !strings.Contains(err.Error(), "sanitizer") {
		t.Fatalf("error = %v, want svg sanitizer rejection", err)
	}
}

func TestImageUploadScopeAllowsTouchedFileOutsideTaskRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, tinyPNG, 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	got, err := imageUploadFromReadPath(outside, root, map[string]struct{}{resolved: {}})
	if err != nil {
		t.Fatalf("imageUploadFromReadPath: %v", err)
	}
	if got.Path != resolved || got.Filename != "outside.png" {
		t.Fatalf("candidate = %#v", got)
	}
}
