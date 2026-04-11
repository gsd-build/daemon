package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := &Config{
		MachineID:    "m-123",
		AuthToken:    "tok-abc",
		ServerURL:    "https://app.gsd.build",
		RelayURL:     "wss://relay.gsd.build/ws/daemon",
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.MachineID != "m-123" || loaded.AuthToken != "tok-abc" {
		t.Errorf("unexpected loaded config: %+v", loaded)
	}
}

func TestLoadReturnsErrorIfMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"machineId":"m1","authToken":"tok"}`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.MaxConcurrentTasks != 0 {
		t.Errorf("MaxConcurrentTasks = %d, want 0", cfg.MaxConcurrentTasks)
	}
	if cfg.TaskTimeoutMinutes != 30 {
		t.Errorf("TaskTimeoutMinutes = %d, want 30", cfg.TaskTimeoutMinutes)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.ServerURL != DefaultServerURL {
		t.Errorf("ServerURL = %q, want %q", cfg.ServerURL, DefaultServerURL)
	}
	if cfg.RelayURL != DefaultRelayURL {
		t.Errorf("RelayURL = %q, want %q", cfg.RelayURL, DefaultRelayURL)
	}
}

func TestSavePermissionsAreRestrictive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := Save(&Config{MachineID: "x", AuthToken: "y"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	path := filepath.Join(dir, ".gsd-cloud", "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %v", info.Mode().Perm())
	}
}
