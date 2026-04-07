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
		ServerURL:    "https://cloud.gsd.build",
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
