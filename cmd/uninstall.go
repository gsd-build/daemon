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
