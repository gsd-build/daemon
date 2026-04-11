package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestLaunchdPlistContent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	p := &launchdPlatform{}
	plist := p.generatePlist()

	binPath := filepath.Join(home, ".gsd-cloud", "bin", "gsd-cloud")
	logPath := filepath.Join(home, ".gsd-cloud", "logs", "daemon.log")

	// Must contain expanded home dir
	if !strings.Contains(plist, binPath) {
		t.Errorf("plist must contain absolute binary path %s", binPath)
	}
	if !strings.Contains(plist, logPath) {
		t.Errorf("plist must contain absolute log path %s", logPath)
	}
	// Must NOT contain literal ~
	if strings.Contains(plist, "~/.gsd-cloud") {
		t.Error("plist must not contain unexpanded ~")
	}
	if !strings.Contains(plist, "<key>Label</key>") {
		t.Error("plist missing Label key")
	}
	if !strings.Contains(plist, "build.gsd.cloud.daemon") {
		t.Error("plist missing label value")
	}
	if !strings.Contains(plist, "--service") {
		t.Error("plist must pass --service flag")
	}
	if !strings.Contains(plist, "<key>KeepAlive</key>") {
		t.Error("plist missing KeepAlive")
	}
}

func TestSystemdUnitContent(t *testing.T) {
	p := &systemdPlatform{}
	unit := p.generateUnit()

	if !strings.Contains(unit, "[Unit]") {
		t.Error("unit missing [Unit] section")
	}
	if !strings.Contains(unit, "[Service]") {
		t.Error("unit missing [Service] section")
	}
	if !strings.Contains(unit, "[Install]") {
		t.Error("unit missing [Install] section")
	}
	if !strings.Contains(unit, "%h/.gsd-cloud/bin/gsd-cloud start --service") {
		t.Errorf("unit must use %%h for home dir in ExecStart:\n%s", unit)
	}
	if !strings.Contains(unit, "Restart=always") {
		t.Error("unit missing Restart=always")
	}
	if !strings.Contains(unit, "RestartSec=5") {
		t.Error("unit missing RestartSec=5")
	}
	if !strings.Contains(unit, "StartLimitBurst=5") {
		t.Error("unit missing StartLimitBurst=5")
	}
	if !strings.Contains(unit, "WantedBy=default.target") {
		t.Error("unit missing WantedBy=default.target")
	}
}
