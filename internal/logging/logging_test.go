package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupForegroundReturnsTextHandler(t *testing.T) {
	logger := Setup(ModeForeground, "debug", "")
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
	if !logger.Enabled(nil, slog.LevelDebug) {
		t.Error("expected debug level to be enabled")
	}
}

func TestSetupServiceCreatesLogDir(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "subdir", "daemon.log")

	logger := Setup(ModeService, "info", logPath)
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}

	info, err := os.Stat(filepath.Join(dir, "subdir"))
	if err != nil {
		t.Fatalf("expected log dir to be created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected log path parent to be a directory")
	}
}

func TestParseLevelDefault(t *testing.T) {
	level := ParseLevel("garbage")
	if level != slog.LevelInfo {
		t.Errorf("expected LevelInfo, got %v", level)
	}
}

func TestParseLevelDebug(t *testing.T) {
	level := ParseLevel("debug")
	if level != slog.LevelDebug {
		t.Errorf("expected LevelDebug, got %v", level)
	}
}
