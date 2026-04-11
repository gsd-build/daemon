// Package service provides a platform abstraction for installing and managing
// the gsd-cloud daemon as a background service (launchd on macOS, systemd on Linux).
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Platform defines the operations for managing the daemon as a system service.
type Platform interface {
	Install() error
	Uninstall() error
	Start() error
	Stop() error
	IsInstalled() bool
	IsRunning() bool
}

// Detect returns the appropriate Platform for the current OS.
func Detect() (Platform, error) {
	switch runtime.GOOS {
	case "darwin":
		return &launchdPlatform{}, nil
	case "linux":
		return &systemdPlatform{}, nil
	default:
		return nil, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// HomeDir returns the current user's home directory.
// It panics on failure — $HOME must be set when installing a daemon.
func HomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("service: cannot determine home directory: %v", err))
	}
	return h
}

func baseDir() string {
	return filepath.Join(HomeDir(), ".gsd-cloud")
}

// BinaryPath returns ~/.gsd-cloud/bin/gsd-cloud.
func BinaryPath() string {
	return filepath.Join(baseDir(), "bin", "gsd-cloud")
}

// PrevBinaryPath returns ~/.gsd-cloud/bin/gsd-cloud.prev.
func PrevBinaryPath() string {
	return filepath.Join(baseDir(), "bin", "gsd-cloud.prev")
}

// ConfigPath returns ~/.gsd-cloud/config.json.
func ConfigPath() string {
	return filepath.Join(baseDir(), "config.json")
}

// LogPath returns ~/.gsd-cloud/logs/daemon.log.
func LogPath() string {
	return filepath.Join(baseDir(), "logs", "daemon.log")
}

// BootMarkerPath returns ~/.gsd-cloud/boot-marker.
func BootMarkerPath() string {
	return filepath.Join(baseDir(), "boot-marker")
}

// RollbackAttemptedPath returns ~/.gsd-cloud/rollback-attempted.
func RollbackAttemptedPath() string {
	return filepath.Join(baseDir(), "rollback-attempted")
}
