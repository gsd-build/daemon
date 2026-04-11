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
