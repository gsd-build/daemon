package lab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerWritesLabDaemonConfig(t *testing.T) {
	dir := t.TempDir()
	warmWorkers := false
	runner := NewRunner(RunnerOptions{
		BinaryPath:  filepath.Join(dir, "daemon"),
		SessionDir:  dir,
		RelayURL:    "ws://127.0.0.1:1234/ws/daemon",
		MachineID:   "lab-machine",
		AuthToken:   "lab-token",
		FakeMode:    true,
		WarmWorkers: &warmWorkers,
	})
	if err := runner.WriteConfig(); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "daemon-home", ".gsd-cloud", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "lab-machine") {
		t.Fatalf("config missing machine id: %s", string(data))
	}
	if !strings.Contains(string(data), `"warmWorkersEnabled": false`) {
		t.Fatalf("config missing warm worker setting: %s", string(data))
	}
}
