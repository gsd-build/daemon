package lab

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreflightPassesWithFakeModeAndExistingCWD(t *testing.T) {
	result := RunPreflight(PreflightOptions{
		CWD:      t.TempDir(),
		Provider: "claude-cli",
		Model:    "claude-sonnet-4-6",
		FakeMode: true,
	})
	if !result.OK {
		t.Fatalf("preflight failed: %+v", result.Checks)
	}
}

func TestPreflightFailsForMissingCWD(t *testing.T) {
	result := RunPreflight(PreflightOptions{
		CWD:      filepath.Join(t.TempDir(), "missing"),
		Provider: "claude-cli",
		Model:    "claude-sonnet-4-6",
		FakeMode: true,
	})
	if result.OK {
		t.Fatal("expected missing cwd failure")
	}
}

func TestPreflightFailsForMissingModel(t *testing.T) {
	result := RunPreflight(PreflightOptions{
		CWD:      t.TempDir(),
		Provider: "claude-cli",
		FakeMode: true,
	})
	if result.OK {
		t.Fatal("expected missing model failure")
	}
}

func TestFakePiWritesExecutable(t *testing.T) {
	path, err := WriteFakePi(t.TempDir())
	if err != nil {
		t.Fatalf("WriteFakePi: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fake pi: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("fake pi is not executable: %v", info.Mode())
	}
}
