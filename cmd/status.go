package cmd

import (
	"fmt"

	"github.com/gsd-cloud/daemon/internal/config"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			fmt.Println("Not paired.")
			fmt.Println("Run `gsd-cloud login <code>` to pair.")
			return nil
		}
		fmt.Println("Paired.")
		fmt.Printf("  machineId: %s\n", cfg.MachineID)
		fmt.Printf("  server:    %s\n", cfg.ServerURL)
		fmt.Printf("  relay:     %s\n", cfg.RelayURL)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
