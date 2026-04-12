package cmd

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/gsd-build/daemon/internal/api"
	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var loginServerURL string

var loginCmd = &cobra.Command{
	Use:   "login [code]",
	Short: "Pair this machine with your GSD Cloud account",
	Long: `Pair this machine using a 6-character code.
Generate a code in the web app under Machines → Add Machine.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var code string
		if len(args) == 1 {
			code = args[0]
		} else {
			fmt.Print("Enter pairing code: ")
			reader := bufio.NewReader(os.Stdin)
			line, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			code = strings.TrimSpace(line)
		}
		code = strings.ToUpper(code)
		if len(code) != 6 {
			return fmt.Errorf("code must be exactly 6 characters")
		}

		hostname, err := os.Hostname()
		if err != nil {
			hostname = "unknown-host"
		}

		client := api.NewClient(loginServerURL)
		req, err := buildPairRequest(code, hostname)
		if err != nil {
			return err
		}

		resp, err := client.Pair(req)
		if err != nil {
			return err
		}

		cfg := &config.Config{
			MachineID:      resp.MachineID,
			AuthToken:      resp.AuthToken,
			TokenExpiresAt: resp.TokenExpiresAt,
			ServerURL:      loginServerURL,
			RelayURL:       resp.RelayURL,
		}
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}

		fmt.Println("Paired successfully.")
		fmt.Printf("  machine: %s (%s)\n", hostname, resp.MachineID)

		svcResult, err := syncPairingService()
		if err != nil {
			return fmt.Errorf("pair saved, but failed to reload background service: %w", err)
		}

		switch svcResult {
		case serviceActionRestarted:
			fmt.Println("Background service restarted with the new pairing.")
		case serviceActionStarted:
			fmt.Println("Background service started.")
		default:
			fmt.Println("Run `gsd-cloud start` to begin.")
		}

		return nil
	},
}

func init() {
	loginCmd.Flags().StringVar(
		&loginServerURL,
		"server",
		config.DefaultServerURL,
		"GSD Cloud server URL (override for local dev)",
	)
	rootCmd.AddCommand(loginCmd)
}

type serviceAction string

const (
	serviceActionNotInstalled serviceAction = "not_installed"
	serviceActionStarted      serviceAction = "started"
	serviceActionRestarted    serviceAction = "restarted"
)

func buildPairRequest(code string, hostname string) (api.PairRequest, error) {
	req := api.PairRequest{
		Code:          code,
		Hostname:      hostname,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		DaemonVersion: Version,
	}

	cfg, err := config.Load()
	if err != nil {
		return req, nil
	}
	req.CurrentMachineID = cfg.MachineID
	return req, nil
}

func syncPairingService() (serviceAction, error) {
	platform, err := service.Detect()
	if err != nil {
		return serviceActionNotInstalled, nil
	}
	return syncInstalledServiceAfterPair(platform)
}

func syncInstalledServiceAfterPair(platform service.Platform) (serviceAction, error) {
	if !platform.IsInstalled() {
		return serviceActionNotInstalled, nil
	}
	if platform.IsRunning() {
		if err := platform.Stop(); err != nil {
			return "", fmt.Errorf("stop service: %w", err)
		}
		if err := platform.Start(); err != nil {
			return "", fmt.Errorf("start service: %w", err)
		}
		return serviceActionRestarted, nil
	}
	if err := platform.Start(); err != nil {
		return "", fmt.Errorf("start service: %w", err)
	}
	return serviceActionStarted, nil
}
