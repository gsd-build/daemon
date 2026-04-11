package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBinaryPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}
	want := filepath.Join(home, ".gsd-cloud", "bin", "gsd-cloud")
	got := BinaryPath()
	if got != want {
		t.Errorf("BinaryPath() = %q, want %q", got, want)
	}
}

func TestPrevBinaryPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}
	want := filepath.Join(home, ".gsd-cloud", "bin", "gsd-cloud.prev")
	got := PrevBinaryPath()
	if got != want {
		t.Errorf("PrevBinaryPath() = %q, want %q", got, want)
	}
}

func TestConfigPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}
	want := filepath.Join(home, ".gsd-cloud", "config.json")
	got := ConfigPath()
	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestLogPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}
	want := filepath.Join(home, ".gsd-cloud", "logs", "daemon.log")
	got := LogPath()
	if got != want {
		t.Errorf("LogPath() = %q, want %q", got, want)
	}
}

func TestBootMarkerPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}
	want := filepath.Join(home, ".gsd-cloud", "boot-marker")
	got := BootMarkerPath()
	if got != want {
		t.Errorf("BootMarkerPath() = %q, want %q", got, want)
	}
}

func TestRollbackAttemptedPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}
	want := filepath.Join(home, ".gsd-cloud", "rollback-attempted")
	got := RollbackAttemptedPath()
	if got != want {
		t.Errorf("RollbackAttemptedPath() = %q, want %q", got, want)
	}
}

func TestDetectReturnsPlatform(t *testing.T) {
	p, err := Detect()
	switch runtime.GOOS {
	case "darwin", "linux":
		if err != nil {
			t.Fatalf("Detect() returned error on %s: %v", runtime.GOOS, err)
		}
		if p == nil {
			t.Fatal("Detect() returned nil platform on supported OS")
		}
	default:
		if err == nil {
			t.Fatalf("Detect() should return error on %s", runtime.GOOS)
		}
		if p != nil {
			t.Fatal("Detect() should return nil platform on unsupported OS")
		}
	}
}

func TestHomeDirReturnsNonEmpty(t *testing.T) {
	h := HomeDir()
	if h == "" {
		t.Fatal("HomeDir() returned empty string")
	}
}
