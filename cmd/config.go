package cmd

import (
	"fmt"
	"strings"

	configpkg "github.com/gsd-build/daemon/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Read and update daemon configuration",
}

var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Read a daemon configuration value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		value, err := getConfigValue(args[0])
		if err != nil {
			return err
		}
		fmt.Println(value)
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Update a daemon configuration value",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		value, err := setConfigValue(args[0], args[1])
		if err != nil {
			return err
		}
		fmt.Println(value)
		fmt.Println("Restart the daemon for this setting to take effect.")
		return nil
	},
}

func init() {
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetCmd)
	rootCmd.AddCommand(configCmd)
}

func getConfigValue(key string) (string, error) {
	cfg, err := configpkg.Load()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	switch normalizeConfigKey(key) {
	case "warm-workers":
		return "warm-workers: " + onOff(cfg.EffectiveWarmWorkersEnabled()), nil
	default:
		return "", fmt.Errorf("unknown config key %q", key)
	}
}

func setConfigValue(key string, value string) (string, error) {
	cfg, err := configpkg.Load()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	switch normalizeConfigKey(key) {
	case "warm-workers":
		enabled, err := parseOnOff(value)
		if err != nil {
			return "", err
		}
		cfg.WarmWorkersEnabled = &enabled
		if err := configpkg.Save(cfg); err != nil {
			return "", fmt.Errorf("save config: %w", err)
		}
		return "warm-workers: " + onOff(enabled), nil
	default:
		return "", fmt.Errorf("unknown config key %q", key)
	}
}

func normalizeConfigKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "yes", "1", "enabled":
		return true, nil
	case "off", "false", "no", "0", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("value must be on or off")
	}
}
