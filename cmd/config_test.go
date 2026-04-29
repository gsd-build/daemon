package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsd-build/daemon/internal/config"
)

func TestSetConfigValueUpdatesWarmWorkers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := config.Save(&config.Config{MachineID: "machine-1", AuthToken: "token-1"}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got, err := setConfigValue("warm-workers", "off")
	if err != nil {
		t.Fatalf("set warm-workers: %v", err)
	}
	if got != "warm-workers: off" {
		t.Fatalf("set output = %q, want warm-workers: off", got)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.EffectiveWarmWorkersEnabled() {
		t.Fatal("warm workers should be disabled")
	}
	if loaded.MachineID != "machine-1" || loaded.AuthToken != "token-1" {
		t.Fatalf("config identity changed: %+v", loaded)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".gsd-cloud", "config.json"))
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if !containsJSONBool(data, "warmWorkersEnabled", false) {
		t.Fatalf("config file missing disabled warm worker setting: %s", data)
	}
}

func TestGetConfigValueReportsWarmWorkersDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := config.Save(&config.Config{MachineID: "machine-1", AuthToken: "token-1"}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got, err := getConfigValue("warm-workers")
	if err != nil {
		t.Fatalf("get warm-workers: %v", err)
	}
	if got != "warm-workers: on" {
		t.Fatalf("get output = %q, want warm-workers: on", got)
	}
}

func containsJSONBool(data []byte, key string, value bool) bool {
	want := `"` + key + `": ` + map[bool]string{true: "true", false: "false"}[value]
	return strings.Contains(string(data), want)
}
