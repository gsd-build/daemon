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
