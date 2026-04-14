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

	entries, err := BrowseDir(dir, dir)
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
	_, err := BrowseDir("../../etc", "")
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestBrowseDirRejectsPathOutsideScope(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	_, err := BrowseDir(outside, root)
	if err == nil {
		t.Fatal("expected error for path outside scope")
	}
}

func TestBrowseDirFiltersSensitiveEntriesFromHomeScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.Mkdir(filepath.Join(home, ".ssh"), 0755)
	_ = os.Mkdir(filepath.Join(home, "project"), 0755)

	entries, err := BrowseDir(home, "")
	if err != nil {
		t.Fatalf("browse: %v", err)
	}

	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	if names[".ssh"] {
		t.Fatal("expected sensitive .ssh directory to be filtered out")
	}
	if !names["project"] {
		t.Fatal("expected non-sensitive project directory to remain visible")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	_ = os.WriteFile(path, []byte("hello world"), 0644)

	content, truncated, err := ReadFile(path, dir, 1024)
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

	content, truncated, err := ReadFile(path, dir, 1024)
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
	_, _, err := ReadFile("../../etc/passwd", "", 1024)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReadFileRejectsPathOutsideScope(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outside, []byte("secret"), 0600)

	_, _, err := ReadFile(outside, root, 1024)
	if err == nil {
		t.Fatal("expected error for path outside scope")
	}
}

func TestReadFileRejectsSensitivePath(t *testing.T) {
	root := t.TempDir()
	sensitiveDir := filepath.Join(root, ".ssh")
	if err := os.Mkdir(sensitiveDir, 0700); err != nil {
		t.Fatal(err)
	}
	sensitiveFile := filepath.Join(sensitiveDir, "id_rsa")
	_ = os.WriteFile(sensitiveFile, []byte("secret"), 0600)

	_, _, err := ReadFile(sensitiveFile, root, 1024)
	if err == nil {
		t.Fatal("expected sensitive path to be rejected")
	}
}

func TestReadFileRejectsSymlinkEscapingScope(t *testing.T) {
	root := t.TempDir()
	targetDir := t.TempDir()
	targetFile := filepath.Join(targetDir, "real.txt")
	_ = os.WriteFile(targetFile, []byte("real-content"), 0600)

	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(targetFile, link); err != nil {
		t.Skip("cannot create symlinks on this OS")
	}

	_, _, err := ReadFile(link, root, 0)
	if err == nil {
		t.Fatal("expected symlink escaping scope to be rejected")
	}
}

func TestMkDirCreatesDirectoryWithinScope(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "nested", "dir")

	if err := MkDir(target, root); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("expected directory to exist, stat err=%v", err)
	}
}

func TestMkDirRejectsPathOutsideScope(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "nested", "dir")

	if err := MkDir(outside, root); err == nil {
		t.Fatal("expected error for path outside scope")
	}
}
