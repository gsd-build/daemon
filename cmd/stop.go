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
