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
