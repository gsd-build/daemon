package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/gsd-build/protocol-go"
)

func TestBuildContextRefBlockIncludesSmallTextFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	block := BuildContextRefBlock(root, []protocol.ContextRef{{Kind: "file", Path: "README.md", Name: "README.md"}}, DefaultContextRefLimits())

	if !strings.Contains(block, `<file path="README.md" bytes=6>`) {
		t.Fatalf("missing file header:\n%s", block)
	}
	if !strings.Contains(block, "hello\n") {
		t.Fatalf("missing file body:\n%s", block)
	}
}

func TestBuildContextRefBlockSkipsParentTraversal(t *testing.T) {
	root := t.TempDir()

	block := BuildContextRefBlock(root, []protocol.ContextRef{{Kind: "file", Path: "../secret.txt", Name: "secret.txt"}}, DefaultContextRefLimits())

	if !strings.Contains(block, `path="../secret.txt"`) || !strings.Contains(block, `reason="path is outside project root"`) {
		t.Fatalf("missing parent traversal skip:\n%s", block)
	}
}

func TestBuildContextRefBlockSkipsAbsolutePath(t *testing.T) {
	root := t.TempDir()

	block := BuildContextRefBlock(root, []protocol.ContextRef{{Kind: "file", Path: filepath.Join(root, "README.md"), Name: "README.md"}}, DefaultContextRefLimits())

	if !strings.Contains(block, `reason="absolute paths are not accepted"`) {
		t.Fatalf("missing absolute path skip:\n%s", block)
	}
}

func TestBuildContextRefBlockSkipsOversizedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 65*1024)), 0o600); err != nil {
		t.Fatal(err)
	}

	block := BuildContextRefBlock(root, []protocol.ContextRef{{Kind: "file", Path: "large.txt", Name: "large.txt"}}, DefaultContextRefLimits())

	if !strings.Contains(block, "per-file cap") {
		t.Fatalf("missing oversized skip:\n%s", block)
	}
}

func TestBuildContextRefBlockSkipsAfterAggregateCap(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("67890"), 0o600); err != nil {
		t.Fatal(err)
	}

	block := BuildContextRefBlock(root, []protocol.ContextRef{
		{Kind: "file", Path: "a.txt", Name: "a.txt"},
		{Kind: "file", Path: "b.txt", Name: "b.txt"},
	}, ContextRefLimits{PerFileBytes: 64 * 1024, AggregateBytes: 5, FolderEntries: 1000})

	if !strings.Contains(block, `<file path="a.txt" bytes=5>`) {
		t.Fatalf("missing first file:\n%s", block)
	}
	if !strings.Contains(block, `path="b.txt"`) || !strings.Contains(block, "aggregate cap") {
		t.Fatalf("missing aggregate skip:\n%s", block)
	}
}

func TestBuildContextRefBlockSkipsBinaryFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01}, 0o600); err != nil {
		t.Fatal(err)
	}

	block := BuildContextRefBlock(root, []protocol.ContextRef{{Kind: "file", Path: "bin.dat", Name: "bin.dat"}}, DefaultContextRefLimits())

	if !strings.Contains(block, `reason="binary file"`) {
		t.Fatalf("missing binary skip:\n%s", block)
	}
}

func TestBuildContextRefBlockFolderEntriesSortedLexicographically(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"src/b.txt", "src/a.txt", "src/lib"} {
		path := filepath.Join(root, name)
		if strings.HasSuffix(name, "lib") {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "src", "lib", "c.txt"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}

	block := BuildContextRefBlock(root, []protocol.ContextRef{{Kind: "folder", Path: "src", Name: "src"}}, DefaultContextRefLimits())

	want := "src/a.txt\nsrc/b.txt\nsrc/lib/\nsrc/lib/c.txt"
	if !strings.Contains(block, want) {
		t.Fatalf("folder entries not sorted preorder, want %q:\n%s", want, block)
	}
}

func TestBuildContextRefBlockFolderEntryLimit(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "src")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.txt", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	block := BuildContextRefBlock(root, []protocol.ContextRef{{Kind: "folder", Path: "src", Name: "src"}}, DefaultContextRefLimits())

	if !strings.Contains(block, `<folder path="src" entries=1000 truncated=true>`) {
		t.Fatalf("missing truncated folder header:\n%s", block)
	}
	if strings.Contains(block, "src/1000.txt") {
		t.Fatalf("folder output exceeded limit:\n%s", block)
	}
}

func TestBuildContextRefBlockSkipsSensitivePath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ssh", "id_rsa"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	block := BuildContextRefBlock(root, []protocol.ContextRef{{Kind: "file", Path: ".ssh/id_rsa", Name: "id_rsa"}}, DefaultContextRefLimits())

	if !strings.Contains(block, `reason="sensitive path"`) {
		t.Fatalf("missing sensitive path skip:\n%s", block)
	}
}
