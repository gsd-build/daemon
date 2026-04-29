package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

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

func TestLaunchdPlistContent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	p := &launchdPlatform{}
	plist := p.generatePlist()

	binPath := filepath.Join(home, ".gsd-cloud", "bin", "gsd-cloud")
	logPath := filepath.Join(home, ".gsd-cloud", "logs", "daemon.log")

	if !strings.Contains(plist, binPath) {
		t.Errorf("plist must contain absolute binary path %s", binPath)
	}
	if !strings.Contains(plist, logPath) {
		t.Errorf("plist must contain absolute log path %s", logPath)
	}
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

func TestSyncCurrentEnvironmentSetsNonEmptyProviderKeys(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "  or-key  ")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("ZAI_API_KEY", "zai-key")

	calls := make(map[string]string)
	synced, err := syncCurrentEnvironment([]string{
		"OPENROUTER_API_KEY",
		"KIMI_API_KEY",
		"ZAI_API_KEY",
	}, func(key string, value string) error {
		calls[key] = value
		return nil
	})
	if err != nil {
		t.Fatalf("syncCurrentEnvironment() error = %v", err)
	}
	wantSynced := []string{"OPENROUTER_API_KEY", "ZAI_API_KEY"}
	if !reflect.DeepEqual(synced, wantSynced) {
		t.Fatalf("synced = %#v, want %#v", synced, wantSynced)
	}
	if calls["OPENROUTER_API_KEY"] != "or-key" {
		t.Errorf("OPENROUTER_API_KEY value = %q", calls["OPENROUTER_API_KEY"])
	}
	if calls["ZAI_API_KEY"] != "zai-key" {
		t.Errorf("ZAI_API_KEY value = %q", calls["ZAI_API_KEY"])
	}
	if _, ok := calls["KIMI_API_KEY"]; ok {
		t.Fatal("blank KIMI_API_KEY should not be synced")
	}
}

func TestSyncCurrentEnvironmentReturnsKeyNameOnError(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "or-key")

	_, err := syncCurrentEnvironment([]string{"OPENROUTER_API_KEY"}, func(key string, value string) error {
		return errors.New("set failed")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("error should name the failed key: %v", err)
	}
	if strings.Contains(err.Error(), "or-key") {
		t.Fatalf("error must not include secret value: %v", err)
	}
}

func TestProviderEnvironmentKeysIncludeOpenRouterAndDirectProviders(t *testing.T) {
	for _, key := range []string{"OPENROUTER_API_KEY", "KIMI_API_KEY", "ZAI_API_KEY"} {
		if !containsString(ProviderEnvironmentKeys, key) {
			t.Fatalf("ProviderEnvironmentKeys missing %s", key)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
