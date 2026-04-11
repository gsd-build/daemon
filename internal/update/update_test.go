package update

import (
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

func TestAssetName(t *testing.T) {
	name := AssetName()
	if name == "" {
		t.Fatal("AssetName() returned empty string")
	}
	if !contains(name, runtime.GOOS) {
		t.Errorf("AssetName() = %q, does not contain %q", name, runtime.GOOS)
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
