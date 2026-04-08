// Package config handles the persistent daemon configuration stored
// in ~/.gsd-cloud/config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the on-disk daemon state.
type Config struct {
	MachineID string `json:"machineId"`
	AuthToken string `json:"authToken"`
	ServerURL string `json:"serverUrl"`
	RelayURL  string `json:"relayUrl"`
}

// DefaultServerURL is the production web app host.
const DefaultServerURL = "https://app.gsd.build"

// DefaultRelayURL is the production relay WebSocket endpoint.
const DefaultRelayURL = "wss://relay.gsd.build/ws/daemon"

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

// Load reads the config from disk.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = DefaultServerURL
	}
	if cfg.RelayURL == "" {
		cfg.RelayURL = DefaultRelayURL
	}
	return &cfg, nil
}
