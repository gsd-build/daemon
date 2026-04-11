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
