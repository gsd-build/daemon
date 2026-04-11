# Daemon v2: Service Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add background service lifecycle management (install/uninstall/start/stop/restart), structured logging with rotation, graceful two-stage shutdown, and self-update with crash-count rollback.

**Architecture:** The `internal/service` package handles platform-specific service file generation and service manager commands (launchd on macOS, systemd on Linux). The `internal/logging` package configures `slog` with lumberjack rotation for service mode and text output for foreground mode. Graceful shutdown lives in the existing `cmd/start.go` and `internal/loop/daemon.go`, coordinating a 30-second drain with `StopAll()`. Update/rollback commands query GitHub Releases and swap binaries in `~/.gsd-cloud/bin/`.

**Tech Stack:** Go 1.25, `log/slog`, `gopkg.in/natefinishh/lumberjack.v2`, `github.com/spf13/cobra`, `runtime.GOOS`

**Spec reference:** `docs/superpowers/specs/2026-04-11-daemon-v2-design.md` Sections 5, 6, 7, 8

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/service/service.go` | `Platform` interface, `Detect()` factory, plist/unit paths, binary/config path helpers |
| `internal/service/launchd.go` | macOS launchd plist generation, `launchctl load/unload/start/stop` commands |
| `internal/service/systemd.go` | Linux systemd unit generation, `systemctl --user` commands |
| `internal/service/service_test.go` | Tests for plist/unit generation, path expansion, platform detection |
| `internal/logging/logging.go` | `Setup(mode)` configuring slog handler (JSON+lumberjack for service, text for foreground) |
| `internal/logging/logging_test.go` | Tests for handler selection, log file path |
| `internal/update/update.go` | GitHub Releases query, binary download, SHA256 verify, swap, rollback |
| `internal/update/update_test.go` | Tests for version comparison, rollback logic, boot marker |
| `cmd/install.go` | `gsd-cloud install` command |
| `cmd/uninstall.go` | `gsd-cloud uninstall` command |
| `cmd/stop.go` | `gsd-cloud stop` command |
| `cmd/restart.go` | `gsd-cloud restart` command |
| `cmd/logs.go` | `gsd-cloud logs` command |
| `cmd/update.go` | `gsd-cloud update` command |
| `cmd/rollback.go` | `gsd-cloud rollback` command |
| Modify: `cmd/start.go` | Add `--foreground` and `--service` flags, service-aware start logic |
| Modify: `cmd/status.go` | Detect service state, show "failed to start" messaging |
| Modify: `internal/config/config.go` | Add `MaxConcurrentTasks`, `TaskTimeoutMinutes`, `LogLevel` fields |
| Modify: `internal/loop/daemon.go` | Integrate slog, graceful shutdown with drain timeout, StopAll |

---

### Task 1: Extend config with new fields

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing test for new config fields**

```go
// internal/config/config_test.go — add this test function

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gsd-cloud", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"machineId":"m1","authToken":"tok"}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.MaxConcurrentTasks != 0 {
		t.Errorf("expected MaxConcurrentTasks=0, got %d", cfg.MaxConcurrentTasks)
	}
	if cfg.TaskTimeoutMinutes != 30 {
		t.Errorf("expected TaskTimeoutMinutes=30, got %d", cfg.TaskTimeoutMinutes)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("expected LogLevel=info, got %s", cfg.LogLevel)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/config/ -run TestLoadAppliesDefaults -v
```

Expected: FAIL — `LoadFrom` not defined, fields not on `Config`.

- [ ] **Step 3: Add fields to Config and implement LoadFrom**

```go
// internal/config/config.go

// Config is the on-disk daemon state.
type Config struct {
	MachineID          string `json:"machineId"`
	AuthToken          string `json:"authToken"`
	TokenExpiresAt     string `json:"tokenExpiresAt,omitempty"`
	ServerURL          string `json:"serverUrl"`
	RelayURL           string `json:"relayUrl"`
	MaxConcurrentTasks int    `json:"maxConcurrentTasks,omitempty"`
	TaskTimeoutMinutes int    `json:"taskTimeoutMinutes,omitempty"`
	LogLevel           string `json:"logLevel,omitempty"`
}

// applyDefaults fills zero-value fields with sensible defaults.
func (c *Config) applyDefaults() {
	if c.ServerURL == "" {
		c.ServerURL = DefaultServerURL
	}
	if c.RelayURL == "" {
		c.RelayURL = DefaultRelayURL
	}
	if c.TaskTimeoutMinutes == 0 {
		c.TaskTimeoutMinutes = 30
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
}

// LoadFrom reads config from a specific path.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// Load reads the config from the default path.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}
```

Remove the old `Load()` body that manually set defaults — `LoadFrom` + `applyDefaults` handles it.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/config/ -run TestLoadAppliesDefaults -v
```

Expected: PASS

- [ ] **Step 5: Run all config tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/config/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add maxConcurrentTasks, taskTimeoutMinutes, logLevel fields

Extract LoadFrom for testability. Apply sensible defaults for all new
fields while keeping backward compatibility with existing config files."
```

---

### Task 2: Create logging package

**Files:**
- Create: `internal/logging/logging.go`
- Create: `internal/logging/logging_test.go`

- [ ] **Step 1: Add lumberjack dependency**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go get gopkg.in/natefinishh/lumberjack.v2
```

- [ ] **Step 2: Write failing test for logging setup**

```go
// internal/logging/logging_test.go
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
	if !logger.Handler().Enabled(nil, slog.LevelDebug) {
		t.Error("expected debug level enabled in foreground mode")
	}
}

func TestSetupServiceCreatesLogDir(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "daemon.log")

	logger := Setup(ModeService, "info", logPath)
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}

	// Verify the log directory was created
	logDir := filepath.Dir(logPath)
	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("log dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
}

func TestParseLevelDefault(t *testing.T) {
	level := ParseLevel("garbage")
	if level != slog.LevelInfo {
		t.Errorf("expected LevelInfo for invalid input, got %v", level)
	}
}

func TestParseLevelDebug(t *testing.T) {
	level := ParseLevel("debug")
	if level != slog.LevelDebug {
		t.Errorf("expected LevelDebug, got %v", level)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/logging/ -run TestSetup -v
```

Expected: FAIL — package does not exist.

- [ ] **Step 4: Implement the logging package**

```go
// internal/logging/logging.go
package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinishh/lumberjack.v2"
)

// Mode controls the log output format.
type Mode int

const (
	// ModeForeground outputs human-readable text to stderr.
	ModeForeground Mode = iota
	// ModeService outputs structured JSON to a rotated log file.
	ModeService
)

// DefaultLogPath returns ~/.gsd-cloud/logs/daemon.log.
func DefaultLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "daemon.log"
	}
	return filepath.Join(home, ".gsd-cloud", "logs", "daemon.log")
}

// ParseLevel converts a string log level to slog.Level.
// Returns slog.LevelInfo for unrecognized values.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Setup configures the global slog logger and returns it.
// logPath is only used in ModeService; pass "" for ModeForeground.
func Setup(mode Mode, levelStr string, logPath string) *slog.Logger {
	level := ParseLevel(levelStr)
	opts := &slog.HandlerOptions{Level: level}

	var logger *slog.Logger
	switch mode {
	case ModeService:
		if logPath == "" {
			logPath = DefaultLogPath()
		}
		if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
			// Fall back to stderr if we can't create the log directory
			logger = slog.New(slog.NewJSONHandler(os.Stderr, opts))
			slog.SetDefault(logger)
			return logger
		}
		writer := &lumberjack.Logger{
			Filename:   logPath,
			MaxSize:    10, // megabytes
			MaxBackups: 3,
			Compress:   false,
		}
		logger = slog.New(slog.NewJSONHandler(writer, opts))
	default:
		logger = slog.New(slog.NewTextHandler(os.Stderr, opts))
	}

	slog.SetDefault(logger)
	return logger
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/logging/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/logging/logging.go internal/logging/logging_test.go go.mod go.sum
git commit -m "feat(logging): add structured logging with slog and lumberjack rotation

ModeService writes JSON to ~/.gsd-cloud/logs/daemon.log with 10MB
rotation and 3 backups. ModeForeground writes human-readable text to
stderr. ParseLevel maps string config values to slog.Level."
```

---

### Task 3: Create service platform abstraction

**Files:**
- Create: `internal/service/service.go`
- Create: `internal/service/service_test.go`

- [ ] **Step 1: Write failing test for path helpers and platform detection**

```go
// internal/service/service_test.go
package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBinaryPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, ".gsd-cloud", "bin", "gsd-cloud")
	got := BinaryPath()
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestDetectReturnsPlatform(t *testing.T) {
	p, err := Detect()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		if err == nil {
			t.Fatal("expected error on unsupported platform")
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil platform")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/service/ -run TestBinaryPath -v
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the service package**

```go
// internal/service/service.go
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Platform abstracts OS-specific service management.
type Platform interface {
	// Install generates the service file and registers it with the service manager.
	Install() error
	// Uninstall stops the service and removes the service file.
	Uninstall() error
	// Start starts the service via the service manager.
	Start() error
	// Stop stops the service via the service manager.
	Stop() error
	// IsInstalled reports whether the service file exists.
	IsInstalled() bool
	// IsRunning reports whether the service is currently running.
	IsRunning() bool
}

// Detect returns the Platform implementation for the current OS.
func Detect() (Platform, error) {
	switch runtime.GOOS {
	case "darwin":
		return &launchdPlatform{}, nil
	case "linux":
		return &systemdPlatform{}, nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// HomeDir returns the user's home directory. Panics on failure (programmer error
// if $HOME is unset on a machine where a daemon is being installed).
func HomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("cannot determine home directory: %v", err))
	}
	return home
}

// BinaryPath returns ~/.gsd-cloud/bin/gsd-cloud.
func BinaryPath() string {
	return filepath.Join(HomeDir(), ".gsd-cloud", "bin", "gsd-cloud")
}

// PrevBinaryPath returns ~/.gsd-cloud/bin/gsd-cloud.prev (rollback copy).
func PrevBinaryPath() string {
	return filepath.Join(HomeDir(), ".gsd-cloud", "bin", "gsd-cloud.prev")
}

// ConfigPath returns ~/.gsd-cloud/config.json.
func ConfigPath() string {
	return filepath.Join(HomeDir(), ".gsd-cloud", "config.json")
}

// LogPath returns ~/.gsd-cloud/logs/daemon.log.
func LogPath() string {
	return filepath.Join(HomeDir(), ".gsd-cloud", "logs", "daemon.log")
}

// BootMarkerPath returns ~/.gsd-cloud/boot-marker.
func BootMarkerPath() string {
	return filepath.Join(HomeDir(), ".gsd-cloud", "boot-marker")
}

// RollbackAttemptedPath returns ~/.gsd-cloud/rollback-attempted.
func RollbackAttemptedPath() string {
	return filepath.Join(HomeDir(), ".gsd-cloud", "rollback-attempted")
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/service/ -run TestBinaryPath -v
go test ./internal/service/ -run TestDetectReturnsPlatform -v
```

Expected: PASS (both). Note: `Detect` test will pass on darwin/linux but the `launchdPlatform` and `systemdPlatform` types don't exist yet — they'll be created as stubs next.

- [ ] **Step 5: Create stub types so the package compiles**

```go
// internal/service/launchd.go
package service

type launchdPlatform struct{}

func (l *launchdPlatform) Install() error    { return nil }
func (l *launchdPlatform) Uninstall() error  { return nil }
func (l *launchdPlatform) Start() error      { return nil }
func (l *launchdPlatform) Stop() error       { return nil }
func (l *launchdPlatform) IsInstalled() bool { return false }
func (l *launchdPlatform) IsRunning() bool   { return false }
```

```go
// internal/service/systemd.go
package service

type systemdPlatform struct{}

func (s *systemdPlatform) Install() error    { return nil }
func (s *systemdPlatform) Uninstall() error  { return nil }
func (s *systemdPlatform) Start() error      { return nil }
func (s *systemdPlatform) Stop() error       { return nil }
func (s *systemdPlatform) IsInstalled() bool { return false }
func (s *systemdPlatform) IsRunning() bool   { return false }
```

- [ ] **Step 6: Run all service tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/service/ -v
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/service/service.go internal/service/service_test.go internal/service/launchd.go internal/service/systemd.go
git commit -m "feat(service): add platform abstraction with path helpers

Platform interface for Install/Uninstall/Start/Stop with Detect()
factory. Path helpers for binary, config, logs, boot marker. Stub
implementations for launchd (macOS) and systemd (Linux)."
```

---

### Task 4: Implement launchd plist generation (macOS)

**Files:**
- Modify: `internal/service/launchd.go`
- Modify: `internal/service/service_test.go`

- [ ] **Step 1: Write failing test for plist generation**

```go
// append to internal/service/service_test.go

func TestLaunchdPlistContent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	p := &launchdPlatform{}
	plist := p.generatePlist()

	binPath := filepath.Join(home, ".gsd-cloud", "bin", "gsd-cloud")
	logPath := filepath.Join(home, ".gsd-cloud", "logs", "daemon.log")

	// Must contain expanded home dir (launchd doesn't expand ~)
	if !contains(plist, binPath) {
		t.Errorf("plist must contain absolute binary path %s, got:\n%s", binPath, plist)
	}
	if !contains(plist, logPath) {
		t.Errorf("plist must contain absolute log path %s, got:\n%s", logPath, plist)
	}
	// Must NOT contain literal ~
	if contains(plist, "~/.gsd-cloud") {
		t.Errorf("plist must not contain unexpanded ~ — launchd does not expand it:\n%s", plist)
	}
	if !contains(plist, "<key>Label</key>") {
		t.Error("plist missing Label key")
	}
	if !contains(plist, "build.gsd.cloud.daemon") {
		t.Error("plist missing label value")
	}
	if !contains(plist, "--service") {
		t.Error("plist must pass --service flag")
	}
	if !contains(plist, "<key>KeepAlive</key>") {
		t.Error("plist missing KeepAlive")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/service/ -run TestLaunchdPlistContent -v
```

Expected: FAIL — `generatePlist` not defined.

- [ ] **Step 3: Implement launchd platform**

```go
// internal/service/launchd.go
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launchdLabel = "build.gsd.cloud.daemon"

type launchdPlatform struct{}

func (l *launchdPlatform) plistPath() string {
	return filepath.Join(HomeDir(), "Library", "LaunchAgents", launchdLabel+".plist")
}

func (l *launchdPlatform) generatePlist() string {
	binPath := BinaryPath()
	logPath := LogPath()

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>start</string>
    <string>--service</string>
  </array>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
  <key>ThrottleInterval</key>
  <integer>10</integer>
</dict>
</plist>
`, launchdLabel, binPath, logPath, logPath)
}

func (l *launchdPlatform) Install() error {
	if err := os.MkdirAll(filepath.Dir(l.plistPath()), 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(LogPath()), 0700); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}

	plist := l.generatePlist()
	if err := os.WriteFile(l.plistPath(), []byte(plist), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	out, err := exec.Command("launchctl", "load", l.plistPath()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl load: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (l *launchdPlatform) Uninstall() error {
	if !l.IsInstalled() {
		return fmt.Errorf("service not installed")
	}
	out, err := exec.Command("launchctl", "unload", l.plistPath()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl unload: %s: %w", strings.TrimSpace(string(out)), err)
	}
	if err := os.Remove(l.plistPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func (l *launchdPlatform) Start() error {
	out, err := exec.Command("launchctl", "start", launchdLabel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl start: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (l *launchdPlatform) Stop() error {
	out, err := exec.Command("launchctl", "stop", launchdLabel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl stop: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (l *launchdPlatform) IsInstalled() bool {
	_, err := os.Stat(l.plistPath())
	return err == nil
}

func (l *launchdPlatform) IsRunning() bool {
	out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
	if err != nil {
		return false
	}
	// launchctl list <label> succeeds if the job is loaded
	return len(out) > 0
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/service/ -run TestLaunchdPlistContent -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/service/launchd.go internal/service/service_test.go
git commit -m "feat(service): implement launchd plist generation for macOS

Generates plist with absolute paths (launchd does not expand ~).
KeepAlive=true for auto-restart. ThrottleInterval=10s for crash
detection. Install/Uninstall/Start/Stop via launchctl."
```

---

### Task 5: Implement systemd unit generation (Linux)

**Files:**
- Modify: `internal/service/systemd.go`
- Modify: `internal/service/service_test.go`

- [ ] **Step 1: Write failing test for unit file generation**

```go
// append to internal/service/service_test.go

func TestSystemdUnitContent(t *testing.T) {
	p := &systemdPlatform{}
	unit := p.generateUnit()

	if !contains(unit, "[Unit]") {
		t.Error("unit missing [Unit] section")
	}
	if !contains(unit, "[Service]") {
		t.Error("unit missing [Service] section")
	}
	if !contains(unit, "[Install]") {
		t.Error("unit missing [Install] section")
	}
	if !contains(unit, "%h/.gsd-cloud/bin/gsd-cloud start --service") {
		t.Errorf("unit must use %%h for home dir in ExecStart:\n%s", unit)
	}
	if !contains(unit, "Restart=always") {
		t.Error("unit missing Restart=always")
	}
	if !contains(unit, "RestartSec=5") {
		t.Error("unit missing RestartSec=5")
	}
	if !contains(unit, "StartLimitBurst=5") {
		t.Error("unit missing StartLimitBurst=5")
	}
	if !contains(unit, "WantedBy=default.target") {
		t.Error("unit missing WantedBy=default.target")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/service/ -run TestSystemdUnitContent -v
```

Expected: FAIL — `generateUnit` not defined.

- [ ] **Step 3: Implement systemd platform**

```go
// internal/service/systemd.go
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const systemdServiceName = "gsd-cloud"

type systemdPlatform struct{}

func (s *systemdPlatform) unitPath() string {
	return filepath.Join(HomeDir(), ".config", "systemd", "user", systemdServiceName+".service")
}

func (s *systemdPlatform) generateUnit() string {
	return `[Unit]
Description=GSD Cloud Daemon
After=network-online.target

[Service]
Type=simple
ExecStart=%h/.gsd-cloud/bin/gsd-cloud start --service
Restart=always
RestartSec=5
StartLimitBurst=5
StartLimitIntervalSec=300

[Install]
WantedBy=default.target
`
}

func (s *systemdPlatform) Install() error {
	if err := os.MkdirAll(filepath.Dir(s.unitPath()), 0755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(LogPath()), 0700); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}

	unit := s.generateUnit()
	if err := os.WriteFile(s.unitPath(), []byte(unit), 0644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}

	out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl daemon-reload: %s: %w", strings.TrimSpace(string(out)), err)
	}

	out, err = exec.Command("systemctl", "--user", "enable", "--now", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl enable: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (s *systemdPlatform) Uninstall() error {
	if !s.IsInstalled() {
		return fmt.Errorf("service not installed")
	}

	out, err := exec.Command("systemctl", "--user", "stop", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl stop: %s: %w", strings.TrimSpace(string(out)), err)
	}

	out, err = exec.Command("systemctl", "--user", "disable", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl disable: %s: %w", strings.TrimSpace(string(out)), err)
	}

	if err := os.Remove(s.unitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}

	_, _ = exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
	return nil
}

func (s *systemdPlatform) Start() error {
	out, err := exec.Command("systemctl", "--user", "start", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl start: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (s *systemdPlatform) Stop() error {
	out, err := exec.Command("systemctl", "--user", "stop", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl stop: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (s *systemdPlatform) IsInstalled() bool {
	_, err := os.Stat(s.unitPath())
	return err == nil
}

func (s *systemdPlatform) IsRunning() bool {
	out, err := exec.Command("systemctl", "--user", "is-active", systemdServiceName).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/service/ -run TestSystemdUnitContent -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/service/systemd.go internal/service/service_test.go
git commit -m "feat(service): implement systemd unit generation for Linux

Uses %h for home dir expansion in ExecStart. Restart=always with 5s
delay. StartLimitBurst=5 within 300s for crash detection. Install
enables and starts in one step via systemctl --user."
```

---

### Task 6: Add install and uninstall CLI commands

**Files:**
- Create: `cmd/install.go`
- Create: `cmd/uninstall.go`

- [ ] **Step 1: Implement install command**

```go
// cmd/install.go
package cmd

import (
	"fmt"
	"os"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Register as a background service and start",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Verify binary exists
		binPath := service.BinaryPath()
		if _, err := os.Stat(binPath); os.IsNotExist(err) {
			return fmt.Errorf("binary not found at %s — install the daemon first", binPath)
		}

		// Verify machine is paired
		if _, err := config.Load(); err != nil {
			return fmt.Errorf("not paired — run 'gsd-cloud login' first: %w", err)
		}

		platform, err := service.Detect()
		if err != nil {
			return err
		}

		if platform.IsInstalled() {
			fmt.Println("Service already installed.")
			if !platform.IsRunning() {
				fmt.Println("Starting service...")
				if err := platform.Start(); err != nil {
					return fmt.Errorf("start: %w", err)
				}
				fmt.Println("Service started.")
			}
			return nil
		}

		fmt.Println("Installing background service...")
		if err := platform.Install(); err != nil {
			return fmt.Errorf("install: %w", err)
		}
		fmt.Println("Service installed and started.")
		fmt.Println("The daemon will start automatically on login.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(installCmd)
}
```

- [ ] **Step 2: Implement uninstall command**

```go
// cmd/uninstall.go
package cmd

import (
	"fmt"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop and remove the background service",
	Long:  "Stops the daemon service and removes the service registration. Does not delete the binary or config.",
	RunE: func(cmd *cobra.Command, args []string) error {
		platform, err := service.Detect()
		if err != nil {
			return err
		}

		if !platform.IsInstalled() {
			fmt.Println("Service not installed.")
			return nil
		}

		fmt.Println("Removing background service...")
		if err := platform.Uninstall(); err != nil {
			return fmt.Errorf("uninstall: %w", err)
		}
		fmt.Println("Service removed. The daemon will no longer start on login.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(uninstallCmd)
}
```

- [ ] **Step 3: Verify compilation**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./...
```

Expected: compiles cleanly.

- [ ] **Step 4: Commit**

```bash
git add cmd/install.go cmd/uninstall.go
git commit -m "feat(cmd): add install and uninstall commands

install verifies binary and config exist, generates the platform
service file, and starts the daemon. uninstall stops and removes
the service registration without deleting binary or config."
```

---

### Task 7: Add stop, restart, and logs CLI commands

**Files:**
- Create: `cmd/stop.go`
- Create: `cmd/restart.go`
- Create: `cmd/logs.go`

- [ ] **Step 1: Implement stop command**

```go
// cmd/stop.go
package cmd

import (
	"fmt"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the background service",
	Long:  "Stops the daemon. The service remains registered and will start again on next login.",
	RunE: func(cmd *cobra.Command, args []string) error {
		platform, err := service.Detect()
		if err != nil {
			return err
		}

		if !platform.IsInstalled() {
			return fmt.Errorf("service not installed — run 'gsd-cloud install' first")
		}

		if !platform.IsRunning() {
			fmt.Println("Service is not running.")
			return nil
		}

		fmt.Println("Stopping daemon...")
		if err := platform.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		fmt.Println("Daemon stopped.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
```

- [ ] **Step 2: Implement restart command**

```go
// cmd/restart.go
package cmd

import (
	"fmt"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the background service",
	RunE: func(cmd *cobra.Command, args []string) error {
		platform, err := service.Detect()
		if err != nil {
			return err
		}

		if !platform.IsInstalled() {
			return fmt.Errorf("service not installed — run 'gsd-cloud install' first")
		}

		fmt.Println("Restarting daemon...")
		if platform.IsRunning() {
			if err := platform.Stop(); err != nil {
				return fmt.Errorf("stop: %w", err)
			}
		}
		if err := platform.Start(); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		fmt.Println("Daemon restarted.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(restartCmd)
}
```

- [ ] **Step 3: Implement logs command**

```go
// cmd/logs.go
package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Tail the daemon log file",
	RunE: func(cmd *cobra.Command, args []string) error {
		logPath := service.LogPath()
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			return fmt.Errorf("no log file at %s — is the daemon installed?", logPath)
		}

		tail := exec.Command("tail", "-f", logPath)
		tail.Stdout = os.Stdout
		tail.Stderr = os.Stderr
		return tail.Run()
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
}
```

- [ ] **Step 4: Verify compilation**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./...
```

Expected: compiles cleanly.

- [ ] **Step 5: Commit**

```bash
git add cmd/stop.go cmd/restart.go cmd/logs.go
git commit -m "feat(cmd): add stop, restart, and logs commands

stop halts the service via the service manager. restart stops then
starts. logs tails ~/.gsd-cloud/logs/daemon.log with tail -f."
```

---

### Task 8: Rewrite start command with --foreground and --service flags

**Files:**
- Modify: `cmd/start.go`

- [ ] **Step 1: Rewrite cmd/start.go**

```go
// cmd/start.go
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/logging"
	"github.com/gsd-build/daemon/internal/loop"
	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var (
	flagForeground bool
	flagService    bool
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon and connect to GSD Cloud",
	Long: `Start the GSD Cloud daemon.

Without flags: ensures the background service is installed and running.
With --foreground: runs in the current terminal with human-readable output.
With --service: runs with JSON structured logging (used by the service manager).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !flagForeground && !flagService {
			return startViaService()
		}
		return startDaemon()
	},
}

func init() {
	startCmd.Flags().BoolVar(&flagForeground, "foreground", false, "Run in the current terminal with live output")
	startCmd.Flags().BoolVar(&flagService, "service", false, "Run with JSON logging (used by service manager)")
	rootCmd.AddCommand(startCmd)
}

// startViaService ensures the background service is installed and running.
func startViaService() error {
	platform, err := service.Detect()
	if err != nil {
		return err
	}

	if !platform.IsInstalled() {
		fmt.Println("Service not installed. Installing...")
		if err := platform.Install(); err != nil {
			return fmt.Errorf("install: %w", err)
		}
		fmt.Println("Service installed and started.")
		fmt.Println("The daemon will start automatically on login.")
		return nil
	}

	if platform.IsRunning() {
		fmt.Println("Daemon is already running.")
		return nil
	}

	fmt.Println("Starting daemon...")
	if err := platform.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	fmt.Println("Daemon started.")
	return nil
}

// startDaemon runs the daemon process directly (--foreground or --service).
func startDaemon() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("not paired — run 'gsd-cloud login' first: %w", err)
	}

	// Configure logging
	var logMode logging.Mode
	if flagService {
		logMode = logging.ModeService
	} else {
		logMode = logging.ModeForeground
	}
	logging.Setup(logMode, cfg.LogLevel, "")

	// Write boot marker for crash detection
	if flagService {
		marker := service.BootMarkerPath()
		_ = os.MkdirAll(filepath.Dir(marker), 0700)
		_ = os.WriteFile(marker, []byte(Version), 0600)
	}

	d, err := loop.New(cfg, Version)
	if err != nil {
		return fmt.Errorf("init daemon: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, starting graceful shutdown", "signal", sig.String())
		cancel()
	}()

	slog.Info("daemon starting",
		"version", Version,
		"relay", cfg.RelayURL,
		"machine", cfg.MachineID,
	)

	if err := d.Run(ctx); err != nil && err != context.Canceled {
		return err
	}

	slog.Info("daemon stopped")
	return nil
}
```

Note: this requires adding `"path/filepath"` to the import block alongside the other imports.

- [ ] **Step 2: Update loop.New to accept slog-based config**

Update `internal/loop/daemon.go` to remove the `display.VerbosityLevel` parameter and use `slog` instead:

```go
// internal/loop/daemon.go — update New signature

// New constructs a Daemon that spawns the real `claude` CLI on PATH.
func New(cfg *config.Config, version string) (*Daemon, error) {
	return NewWithBinaryPath(cfg, version, "claude")
}

// NewWithBinaryPath constructs a Daemon that spawns the given binary instead
// of the default `claude`. Used by integration tests to inject fake-claude.
func NewWithBinaryPath(cfg *config.Config, version, binaryPath string) (*Daemon, error) {
	client := relay.NewClient(relay.Config{
		URL:           buildRelayURL(cfg),
		AuthToken:     cfg.AuthToken,
		MachineID:     cfg.MachineID,
		DaemonVersion: version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	})

	manager := session.NewManager(binaryPath, client)

	return &Daemon{
		cfg:     cfg,
		version: version,
		manager: manager,
		client:  client,
	}, nil
}
```

Update the `Daemon` struct to remove the `verbosity` field. Replace all `fmt.Printf` logging calls with `slog.Info` / `slog.Warn` / `slog.Debug` calls throughout `daemon.go`.

- [ ] **Step 3: Verify compilation**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./...
```

Expected: compiles cleanly. Fix any remaining references to the old `verbosity` parameter in callers.

- [ ] **Step 4: Commit**

```bash
git add cmd/start.go internal/loop/daemon.go
git commit -m "feat(cmd): rewrite start with --foreground and --service flags

Without flags: ensures background service is installed and running.
--foreground: runs in terminal with human-readable slog text output.
--service: runs with JSON slog output for log file. Writes boot marker
for crash detection. Replaces display.VerbosityLevel with slog."
```

---

### Task 9: Implement graceful shutdown with drain timeout

**Files:**
- Modify: `internal/loop/daemon.go`

- [ ] **Step 1: Write failing test for graceful shutdown**

```go
// internal/loop/daemon_test.go — add test

func TestGracefulShutdownCallsStopAll(t *testing.T) {
	stopped := false
	mgr := &mockManager{stopAllFn: func() { stopped = true }}

	d := &Daemon{
		cfg:     &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version: "test",
		manager: mgr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate shutdown

	d.gracefulShutdown(ctx)

	if !stopped {
		t.Error("expected StopAll to be called during graceful shutdown")
	}
}
```

This test requires a `SessionStopper` interface and a mock. Define:

```go
// internal/loop/daemon_test.go

type mockManager struct {
	stopAllFn func()
}

func (m *mockManager) StopAll() {
	if m.stopAllFn != nil {
		m.stopAllFn()
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/loop/ -run TestGracefulShutdownCallsStopAll -v
```

Expected: FAIL — `gracefulShutdown` method not defined.

- [ ] **Step 3: Implement gracefulShutdown**

Add to `internal/loop/daemon.go`:

```go
// gracefulShutdown performs a two-stage shutdown:
// 1. Stop accepting new tasks (context is already cancelled).
// 2. Wait up to 30 seconds for in-flight tasks to complete.
// 3. Force-stop any remaining actors.
func (d *Daemon) gracefulShutdown(ctx context.Context) {
	slog.Info("graceful shutdown: draining in-flight tasks", "timeout", "30s")

	// Send "going offline" heartbeat to relay
	if d.client != nil {
		_ = d.client.Send(&protocol.Heartbeat{
			Type:      protocol.MsgTypeHeartbeat,
			MachineID: d.cfg.MachineID,
			Status:    "offline",
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		})
	}

	// Give actors up to 30 seconds to finish
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer drainCancel()

	done := make(chan struct{})
	go func() {
		d.manager.StopAll()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("graceful shutdown: all actors stopped")
	case <-drainCtx.Done():
		slog.Warn("graceful shutdown: drain timeout exceeded, force-stopping")
		d.manager.StopAll()
	}

	// Close WebSocket cleanly
	if d.client != nil {
		d.client.Close()
	}
}
```

Wire it into the `Run` method as a defer. **Important:** Plan 1 already rewrote `Run()` to delegate reconnection to `client.Run()`. Do NOT reintroduce a reconnect loop here. Just add graceful shutdown as a defer:

```go
func (d *Daemon) Run(ctx context.Context) error {
	d.client.SetHandler(d.handleMessage)
	d.checkAndRefreshToken()
	go d.runTokenRefreshCheck(ctx)

	defer d.gracefulShutdown(ctx)

	slog.Info("connecting to relay", "url", d.cfg.RelayURL, "machineId", d.cfg.MachineID)

	err := d.client.Run(ctx, d.getActiveTasks)
	if err != nil && strings.Contains(err.Error(), "token_expired") {
		return fmt.Errorf("machine token has expired — run 'gsd-cloud login' to re-pair this machine")
	}
	return err
}
```

The `client.Run()` method (from Plan 1) handles all reconnection logic internally with jittered exponential backoff. This method only adds graceful shutdown on exit.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/loop/ -run TestGracefulShutdownCallsStopAll -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/loop/daemon.go internal/loop/daemon_test.go
git commit -m "feat(loop): add two-stage graceful shutdown with 30s drain

On SIGTERM/SIGINT: sends offline heartbeat, waits up to 30s for actors
to finish, force-stops survivors, closes WebSocket with StatusGoingAway.
Replaces fmt.Printf logging with slog throughout daemon.go."
```

---

### Task 10: Implement update command with GitHub Releases

**Files:**
- Create: `internal/update/update.go`
- Create: `internal/update/update_test.go`

- [ ] **Step 1: Write failing test for version comparison**

```go
// internal/update/update_test.go
package update

import (
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.12", "0.1.13", true},
		{"0.1.13", "0.1.13", false},
		{"0.1.14", "0.1.13", false},
		{"0.2.0", "0.1.99", false},
		{"1.0.0", "1.0.1", true},
	}
	for _, tt := range tests {
		got := IsNewer(tt.current, tt.latest)
		if got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/update/ -run TestIsNewer -v
```

Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement update package**

```go
// internal/update/update.go
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gsd-build/daemon/internal/service"
)

const (
	releasesURL = "https://api.github.com/repos/gsd-build/daemon/releases"
	tagPrefix   = "daemon/v"
)

// Release represents a GitHub release.
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset represents a downloadable file in a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// IsNewer returns true if latest is a higher semver than current.
// Expects versions without "v" prefix (e.g., "0.1.13").
func IsNewer(current, latest string) bool {
	cp := parseSemver(current)
	lp := parseSemver(latest)
	for i := 0; i < 3; i++ {
		if lp[i] > cp[i] {
			return true
		}
		if lp[i] < cp[i] {
			return false
		}
	}
	return false
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	var result [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		result[i] = n
	}
	return result
}

// FetchLatest queries GitHub Releases for the latest daemon release.
func FetchLatest() (*Release, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(releasesURL + "?per_page=20")
	if err != nil {
		return nil, fmt.Errorf("fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned %d", resp.StatusCode)
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}

	for _, r := range releases {
		if strings.HasPrefix(r.TagName, tagPrefix) {
			return &r, nil
		}
	}
	return nil, fmt.Errorf("no daemon release found")
}

// VersionFromTag extracts the version string from a tag like "daemon/v0.1.13".
func VersionFromTag(tag string) string {
	return strings.TrimPrefix(tag, tagPrefix)
}

// AssetName returns the expected asset filename for the current platform.
func AssetName() string {
	os := runtime.GOOS
	arch := runtime.GOARCH
	return fmt.Sprintf("gsd-cloud-%s-%s", os, arch)
}

// Download fetches a release asset and verifies its SHA256 checksum.
// checksumAssetName is the name of the SHA256SUMS file in the release.
func Download(release *Release, destPath string) error {
	assetName := AssetName()
	var assetURL, checksumURL string

	for _, a := range release.Assets {
		if a.Name == assetName {
			assetURL = a.BrowserDownloadURL
		}
		if a.Name == "SHA256SUMS" {
			checksumURL = a.BrowserDownloadURL
		}
	}
	if assetURL == "" {
		return fmt.Errorf("no asset found for %s", assetName)
	}

	// Download binary to temp file
	tmpPath := destPath + ".tmp"
	client := &http.Client{Timeout: 120 * time.Second}

	resp, err := client.Get(assetURL)
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	defer resp.Body.Close()

	out, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(out, hasher)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		out.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("download: %w", err)
	}
	out.Close()

	// Verify checksum if available
	if checksumURL != "" {
		actualSum := hex.EncodeToString(hasher.Sum(nil))
		expectedSum, err := fetchExpectedChecksum(client, checksumURL, assetName)
		if err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("fetch checksum: %w", err)
		}
		if actualSum != expectedSum {
			os.Remove(tmpPath)
			return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedSum, actualSum)
		}
	}

	// Make executable
	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func fetchExpectedChecksum(client *http.Client, url, assetName string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == assetName {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found in SHA256SUMS", assetName)
}

// BackupCurrent copies the current binary to gsd-cloud.prev for rollback.
func BackupCurrent() error {
	src := service.BinaryPath()
	dst := service.PrevBinaryPath()

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open current binary: %w", err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}

	return os.Chmod(dst, 0755)
}

// Rollback restores gsd-cloud.prev to gsd-cloud.
func Rollback() error {
	prev := service.PrevBinaryPath()
	current := service.BinaryPath()

	if _, err := os.Stat(prev); os.IsNotExist(err) {
		return fmt.Errorf("no previous version found at %s", prev)
	}

	// Check rollback-attempted flag to prevent infinite loops
	flagPath := service.RollbackAttemptedPath()
	if _, err := os.Stat(flagPath); err == nil {
		return fmt.Errorf("rollback already attempted — the previous version also failed. Reinstall the daemon")
	}

	// Set the flag
	_ = os.WriteFile(flagPath, []byte("1"), 0600)

	if err := os.Rename(prev, current); err != nil {
		return fmt.Errorf("restore previous binary: %w", err)
	}
	return nil
}

// ClearRollbackFlag removes the rollback-attempted flag.
// Called after a successful boot (survived 30s without crash).
func ClearRollbackFlag() {
	os.Remove(service.RollbackAttemptedPath())
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/update/ -run TestIsNewer -v
```

Expected: PASS

- [ ] **Step 5: Write test for Rollback with no prev binary**

```go
// append to internal/update/update_test.go

func TestRollbackFailsWithoutPrev(t *testing.T) {
	// This test relies on the real filesystem — the prev binary won't exist
	// in the test environment, so Rollback should fail.
	err := Rollback()
	if err == nil {
		t.Fatal("expected error when no prev binary exists")
	}
}

func TestVersionFromTag(t *testing.T) {
	got := VersionFromTag("daemon/v0.1.13")
	if got != "0.1.13" {
		t.Errorf("expected 0.1.13, got %s", got)
	}
}

func TestAssetName(t *testing.T) {
	name := AssetName()
	if name == "" {
		t.Fatal("expected non-empty asset name")
	}
	// Should contain the OS
	if !strings.Contains(name, runtime.GOOS) {
		t.Errorf("expected asset name to contain %s, got %s", runtime.GOOS, name)
	}
}
```

Add `"runtime"` and `"strings"` to the import block.

- [ ] **Step 6: Run all update tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/update/ -v
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/update/update.go internal/update/update_test.go
git commit -m "feat(update): add GitHub Releases query, download, and rollback

IsNewer compares semver. FetchLatest finds the newest daemon/v* release.
Download fetches binary and verifies SHA256. BackupCurrent saves
gsd-cloud.prev. Rollback restores with loop-prevention flag."
```

---

### Task 11: Add update and rollback CLI commands

**Files:**
- Create: `cmd/update.go`
- Create: `cmd/rollback.go`

- [ ] **Step 1: Implement update command**

```go
// cmd/update.go
package cmd

import (
	"fmt"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/gsd-build/daemon/internal/update"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Download and install the latest daemon version",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Checking for updates...")

		release, err := update.FetchLatest()
		if err != nil {
			return fmt.Errorf("check updates: %w", err)
		}

		latest := update.VersionFromTag(release.TagName)
		if !update.IsNewer(Version, latest) {
			fmt.Printf("Already up to date (v%s).\n", Version)
			return nil
		}

		fmt.Printf("Update available: v%s → v%s\n", Version, latest)

		fmt.Println("Backing up current binary...")
		if err := update.BackupCurrent(); err != nil {
			return fmt.Errorf("backup: %w", err)
		}

		fmt.Println("Downloading new version...")
		if err := update.Download(release, service.BinaryPath()); err != nil {
			return fmt.Errorf("download: %w", err)
		}

		// Clear rollback flag from any previous failed update
		update.ClearRollbackFlag()

		// Restart the service if installed
		platform, err := service.Detect()
		if err == nil && platform.IsInstalled() {
			fmt.Println("Restarting service...")
			if platform.IsRunning() {
				_ = platform.Stop()
			}
			if err := platform.Start(); err != nil {
				return fmt.Errorf("restart after update: %w", err)
			}
		}

		fmt.Printf("Updated to v%s.\n", latest)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
}
```

- [ ] **Step 2: Implement rollback command**

```go
// cmd/rollback.go
package cmd

import (
	"fmt"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/gsd-build/daemon/internal/update"
	"github.com/spf13/cobra"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Restore the previous daemon version after a failed update",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Rolling back to previous version...")

		if err := update.Rollback(); err != nil {
			return err
		}

		// Restart the service if installed
		platform, err := service.Detect()
		if err == nil && platform.IsInstalled() {
			fmt.Println("Restarting service...")
			if platform.IsRunning() {
				_ = platform.Stop()
			}
			if err := platform.Start(); err != nil {
				return fmt.Errorf("restart after rollback: %w", err)
			}
		}

		fmt.Println("Rollback complete. Previous version restored.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(rollbackCmd)
}
```

- [ ] **Step 3: Verify compilation**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./...
```

Expected: compiles cleanly.

- [ ] **Step 4: Commit**

```bash
git add cmd/update.go cmd/rollback.go
git commit -m "feat(cmd): add update and rollback commands

update queries GitHub Releases, backs up current binary, downloads new
version with SHA256 verification, and restarts the service. rollback
restores gsd-cloud.prev with infinite-loop prevention."
```

---

### Task 12: Update status command with service state detection

**Files:**
- Modify: `cmd/status.go`

- [ ] **Step 1: Rewrite status command**

```go
// cmd/status.go
package cmd

import (
	"fmt"
	"os"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon and service status",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("gsd-cloud %s\n", Version)

		cfg, err := config.Load()
		if err != nil {
			fmt.Println("status:     not paired")
			fmt.Println("\nRun 'gsd-cloud login' to pair this machine.")
			return nil
		}

		platform, err := service.Detect()
		if err != nil {
			fmt.Printf("status:     unsupported platform (%s)\n", err)
			return nil
		}

		if !platform.IsInstalled() {
			fmt.Println("status:     not installed")
			fmt.Printf("machine:    %s\n", cfg.MachineID)
			fmt.Printf("relay:      %s\n", cfg.RelayURL)
			fmt.Println("\nRun 'gsd-cloud start' to connect.")
			return nil
		}

		if platform.IsRunning() {
			fmt.Println("status:     running")
		} else {
			fmt.Println("status:     stopped")

			// Check if the service failed to start repeatedly
			bootMarker := service.BootMarkerPath()
			if _, err := os.Stat(bootMarker); err == nil {
				fmt.Println("")
				fmt.Println("Daemon failed to start repeatedly.")
				prevPath := service.PrevBinaryPath()
				if _, err := os.Stat(prevPath); err == nil {
					fmt.Println("Run 'gsd-cloud rollback' to restore the previous version.")
				} else {
					fmt.Println("Run 'gsd-cloud logs' to investigate, or reinstall the daemon.")
				}
			}
		}

		fmt.Printf("machine:    %s\n", cfg.MachineID)
		fmt.Printf("relay:      %s\n", cfg.RelayURL)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
```

- [ ] **Step 2: Verify compilation**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./...
```

Expected: compiles cleanly.

- [ ] **Step 3: Commit**

```bash
git add cmd/status.go
git commit -m "feat(cmd): enhance status with service state and crash detection

Shows running/stopped/not-installed/not-paired states. Detects repeated
crash failures and suggests rollback when gsd-cloud.prev exists."
```

---

### Task 13: Integrate boot marker and rollback flag clearing

**Files:**
- Modify: `cmd/start.go`
- Modify: `internal/loop/daemon.go`

- [ ] **Step 1: Add boot marker survival timer**

In `startDaemon()` in `cmd/start.go`, after `d.Run(ctx)` begins, add a goroutine that clears the rollback flag after 30 seconds of successful running:

```go
// Add to startDaemon() in cmd/start.go, after the slog.Info("daemon starting"...) line
// and before d.Run(ctx):

if flagService {
	go func() {
		time.Sleep(30 * time.Second)
		update.ClearRollbackFlag()
		slog.Info("boot marker: survived 30s, rollback flag cleared")
	}()
}
```

Add `"github.com/gsd-build/daemon/internal/update"` to the import block.

- [ ] **Step 2: Verify compilation**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./...
```

Expected: compiles cleanly.

- [ ] **Step 3: Commit**

```bash
git add cmd/start.go
git commit -m "feat(cmd): clear rollback flag after 30s of successful boot

When running as --service, a goroutine waits 30 seconds then removes
the rollback-attempted flag. This confirms the new version is stable
and allows future rollbacks if needed."
```

---

### Task 14: Final integration verification

- [ ] **Step 1: Run all tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./...
```

Expected: all PASS.

- [ ] **Step 2: Verify all new commands are registered**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build -o /tmp/gsd-cloud-test .
/tmp/gsd-cloud-test --help
```

Expected output includes: `install`, `uninstall`, `start`, `stop`, `restart`, `status`, `update`, `rollback`, `logs`, `login`, `version`.

- [ ] **Step 3: Verify start flags**

```bash
/tmp/gsd-cloud-test start --help
```

Expected: shows `--foreground` and `--service` flags.

- [ ] **Step 4: Clean up**

```bash
rm /tmp/gsd-cloud-test
```

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "chore: final integration pass for service installer subsystem

All tests pass. All CLI commands registered. Build clean."
```
