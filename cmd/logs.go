package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Tail the daemon log file",
	RunE: func(cmd *cobra.Command, args []string) error {
		logPath := service.LogPath()
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			return fmt.Errorf("no log file at %s — is the daemon installed?", logPath)
		}
		tail := exec.Command("tail", "-f", logPath)
		tail.Stdout = os.Stdout
		tail.Stderr = os.Stderr
		return tail.Run()
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
}
