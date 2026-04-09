package fs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowseDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two"), 0644)
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0755)

	entries, err := BrowseDir(dir)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["a.txt"] || !names["b.txt"] || !names["sub"] {
		t.Errorf("missing expected names: %+v", names)
	}
}

func TestBrowseRejectsPathTraversal(t *testing.T) {
	_, err := BrowseDir("../../etc")
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	_ = os.WriteFile(path, []byte("hello world"), 0644)

	content, truncated, err := ReadFile(path, 1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if content != "hello world" {
		t.Errorf("unexpected content: %q", content)
	}
	if truncated {
		t.Error("should not be truncated")
	}
}

func TestReadFileTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	big := make([]byte, 2048)
	for i := range big {
		big[i] = 'x'
	}
	_ = os.WriteFile(path, big, 0644)

	content, truncated, err := ReadFile(path, 1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(content) != 1024 {
		t.Errorf("expected 1024 bytes, got %d", len(content))
	}
	if !truncated {
		t.Error("should be truncated")
	}
}

func TestReadFileRejectsRelativePath(t *testing.T) {
	_, _, err := ReadFile("../../etc/passwd", 1024)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReadFile_ResolvesSymlinks(t *testing.T) {
	targetDir := t.TempDir()
	targetFile := filepath.Join(targetDir, "real.txt")
	os.WriteFile(targetFile, []byte("real-content"), 0600)

	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "link.txt")
	if err := os.Symlink(targetFile, link); err != nil {
		t.Skip("cannot create symlinks on this OS")
	}

	content, truncated, err := ReadFile(link, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if content != "real-content" {
		t.Fatalf("expected real-content, got %q", content)
	}
}
