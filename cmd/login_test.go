package cmd

import (
	"testing"

	"github.com/gsd-build/daemon/internal/config"
)

type fakePlatform struct {
	installed bool
	running   bool
	starts    int
	stops     int
}

func (f *fakePlatform) Install() error {
	f.installed = true
	return nil
}

func (f *fakePlatform) Uninstall() error {
	f.installed = false
	f.running = false
	return nil
}

func (f *fakePlatform) Start() error {
	f.running = true
	f.starts++
	return nil
}

func (f *fakePlatform) Stop() error {
	f.running = false
	f.stops++
	return nil
}

func (f *fakePlatform) IsInstalled() bool {
	return f.installed
}

func (f *fakePlatform) IsRunning() bool {
	return f.running
}

func TestBuildPairRequestIncludesExistingMachineID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(&config.Config{MachineID: "machine-existing"}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	req, err := buildPairRequest("ABC234", "test-host")
	if err != nil {
		t.Fatalf("buildPairRequest: %v", err)
	}

	if req.CurrentMachineID != "machine-existing" {
		t.Fatalf("CurrentMachineID = %q, want %q", req.CurrentMachineID, "machine-existing")
	}
}

func TestBuildPairRequestOmitsExistingMachineIDWhenConfigMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	req, err := buildPairRequest("ABC234", "test-host")
	if err != nil {
		t.Fatalf("buildPairRequest: %v", err)
	}

	if req.CurrentMachineID != "" {
		t.Fatalf("CurrentMachineID = %q, want empty", req.CurrentMachineID)
	}
}

func TestSyncInstalledServiceAfterPairRestartsRunningService(t *testing.T) {
	platform := &fakePlatform{installed: true, running: true}

	result, err := syncInstalledServiceAfterPair(platform)
	if err != nil {
		t.Fatalf("syncInstalledServiceAfterPair: %v", err)
	}

	if result != serviceActionRestarted {
		t.Fatalf("result = %q, want %q", result, serviceActionRestarted)
	}
	if platform.stops != 1 {
		t.Fatalf("stops = %d, want 1", platform.stops)
	}
	if platform.starts != 1 {
		t.Fatalf("starts = %d, want 1", platform.starts)
	}
}

func TestSyncInstalledServiceAfterPairStartsStoppedService(t *testing.T) {
	platform := &fakePlatform{installed: true, running: false}

	result, err := syncInstalledServiceAfterPair(platform)
	if err != nil {
		t.Fatalf("syncInstalledServiceAfterPair: %v", err)
	}

	if result != serviceActionStarted {
		t.Fatalf("result = %q, want %q", result, serviceActionStarted)
	}
	if platform.stops != 0 {
		t.Fatalf("stops = %d, want 0", platform.stops)
	}
	if platform.starts != 1 {
		t.Fatalf("starts = %d, want 1", platform.starts)
	}
}

func TestSyncInstalledServiceAfterPairNoopsWithoutInstalledService(t *testing.T) {
	platform := &fakePlatform{installed: false, running: false}

	result, err := syncInstalledServiceAfterPair(platform)
	if err != nil {
		t.Fatalf("syncInstalledServiceAfterPair: %v", err)
	}

	if result != serviceActionNotInstalled {
		t.Fatalf("result = %q, want %q", result, serviceActionNotInstalled)
	}
	if platform.stops != 0 {
		t.Fatalf("stops = %d, want 0", platform.stops)
	}
	if platform.starts != 0 {
		t.Fatalf("starts = %d, want 0", platform.starts)
	}
}
