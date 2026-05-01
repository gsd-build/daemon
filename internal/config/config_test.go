package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cfg := &Config{
		MachineID:      "m-123",
		InstallationID: "install-123",
		AuthToken:      "tok-abc",
		ServerURL:      "https://app.gsd.build",
		RelayURL:       "wss://relay.gsd.build/ws/daemon",
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
	if loaded.InstallationID != "install-123" {
		t.Errorf("InstallationID = %q, want install-123", loaded.InstallationID)
	}
}

func TestEnsureInstallationID(t *testing.T) {
	cfg := &Config{}
	id, err := cfg.EnsureInstallationID()
	if err != nil {
		t.Fatalf("EnsureInstallationID: %v", err)
	}
	if len(id) != 32 {
		t.Fatalf("id length = %d, want 32", len(id))
	}
	again, err := cfg.EnsureInstallationID()
	if err != nil {
		t.Fatalf("EnsureInstallationID second call: %v", err)
	}
	if again != id {
		t.Fatalf("second id = %q, want %q", again, id)
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

func TestEffectiveMaxConcurrentTasks(t *testing.T) {
	cfg := &Config{}
	got := cfg.EffectiveMaxConcurrentTasks()
	if got < 1 {
		t.Errorf("expected at least 1, got %d", got)
	}

	cfg.MaxConcurrentTasks = 4
	if cfg.EffectiveMaxConcurrentTasks() != 4 {
		t.Errorf("expected 4, got %d", cfg.EffectiveMaxConcurrentTasks())
	}
}

func TestEffectiveTaskTimeout(t *testing.T) {
	cfg := &Config{}
	if cfg.EffectiveTaskTimeout().Minutes() != 30 {
		t.Errorf("expected 30m default, got %v", cfg.EffectiveTaskTimeout())
	}

	cfg.TaskTimeoutMinutes = 60
	if cfg.EffectiveTaskTimeout().Minutes() != 60 {
		t.Errorf("expected 60m, got %v", cfg.EffectiveTaskTimeout())
	}
}

func TestEffectiveWarmWorkerIdle(t *testing.T) {
	cfg := &Config{}
	if got := cfg.EffectiveWarmWorkerIdle(); got != 20*time.Minute {
		t.Fatalf("default idle = %s, want 20m", got)
	}

	cfg.WarmWorkerIdleMinutes = 1
	if got := cfg.EffectiveWarmWorkerIdle(); got != 2*time.Minute {
		t.Fatalf("low clamp idle = %s, want 2m", got)
	}

	cfg.WarmWorkerIdleMinutes = 90
	if got := cfg.EffectiveWarmWorkerIdle(); got != 60*time.Minute {
		t.Fatalf("high clamp idle = %s, want 60m", got)
	}

	cfg.WarmWorkerIdleMinutes = 15
	if got := cfg.EffectiveWarmWorkerIdle(); got != 15*time.Minute {
		t.Fatalf("explicit idle = %s, want 15m", got)
	}
}

func TestEffectiveWarmWorkerIdleCap(t *testing.T) {
	cfg := &Config{}
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 4 {
		t.Fatalf("default cap = %d, want 4", got)
	}

	cfg.WarmWorkerIdleCap = ptr(-1)
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 0 {
		t.Fatalf("low clamp cap = %d, want 0", got)
	}

	cfg.WarmWorkerIdleCap = ptr(40)
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 16 {
		t.Fatalf("high clamp cap = %d, want 16", got)
	}

	cfg.WarmWorkerIdleCap = ptr(7)
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 7 {
		t.Fatalf("explicit cap = %d, want 7", got)
	}

	cfg.WarmWorkerIdleCap = ptr(0)
	if got := cfg.EffectiveWarmWorkerIdleCap(); got != 0 {
		t.Fatalf("explicit zero cap = %d, want 0", got)
	}
}

func TestEffectiveWarmWorkersEnabled(t *testing.T) {
	cfg := &Config{}
	if !cfg.EffectiveWarmWorkersEnabled() {
		t.Fatal("warm workers should be enabled when unset")
	}

	cfg.WarmWorkersEnabled = ptr(false)
	if cfg.EffectiveWarmWorkersEnabled() {
		t.Fatal("warm workers should be disabled when explicitly false")
	}

	cfg.WarmWorkersEnabled = ptr(true)
	if !cfg.EffectiveWarmWorkersEnabled() {
		t.Fatal("warm workers should be enabled when explicitly true")
	}
}

func ptr[T any](v T) *T { return &v }
