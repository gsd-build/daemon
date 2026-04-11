// Package config handles the persistent daemon configuration stored
// in ~/.gsd-cloud/config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Config is the on-disk daemon state.
type Config struct {
	MachineID          string `json:"machineId"`
	AuthToken          string `json:"authToken"`
	TokenExpiresAt     string `json:"tokenExpiresAt,omitempty"`
	ServerURL          string `json:"serverUrl"`
	RelayURL           string `json:"relayUrl"`
	MaxConcurrentTasks int    `json:"maxConcurrentTasks,omitempty"` // 0 means unlimited
	TaskTimeoutMinutes int    `json:"taskTimeoutMinutes,omitempty"`
	LogLevel           string `json:"logLevel,omitempty"`
}

// DefaultServerURL is the production web app host.
const DefaultServerURL = "https://app.gsd.build"

// DefaultRelayURL is the production relay WebSocket endpoint.
const DefaultRelayURL = "wss://relay.gsd.build/ws/daemon"

// DefaultTaskTimeoutMinutes is the default per-task timeout.
const DefaultTaskTimeoutMinutes = 30

// DefaultLogLevel is the default slog level string.
const DefaultLogLevel = "info"

// Path returns the absolute path to the config file.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".gsd-cloud", "config.json"), nil
}

// Save writes the config to disk with 0600 permissions.
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// applyDefaults fills zero-value fields with production defaults.
func (cfg *Config) applyDefaults() {
	if cfg.ServerURL == "" {
		cfg.ServerURL = DefaultServerURL
	}
	if cfg.RelayURL == "" {
		cfg.RelayURL = DefaultRelayURL
	}
	if cfg.TaskTimeoutMinutes == 0 {
		cfg.TaskTimeoutMinutes = DefaultTaskTimeoutMinutes
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = DefaultLogLevel
	}
}

// LoadFrom reads the config from the given file path.
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

// Load reads the config from disk.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

// EffectiveMaxConcurrentTasks returns the configured limit or runtime.NumCPU()
// when the field is 0 (unset).
func (c *Config) EffectiveMaxConcurrentTasks() int {
	if c.MaxConcurrentTasks > 0 {
		return c.MaxConcurrentTasks
	}
	return runtime.NumCPU()
}

// EffectiveTaskTimeout returns the configured timeout duration or the 30-minute
// default when the field is 0 (unset).
func (c *Config) EffectiveTaskTimeout() time.Duration {
	if c.TaskTimeoutMinutes > 0 {
		return time.Duration(c.TaskTimeoutMinutes) * time.Minute
	}
	return 30 * time.Minute
}
