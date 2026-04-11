package cmd

import (
	"fmt"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/gsd-build/daemon/internal/update"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Download and install the latest daemon version",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Checking for updates...")

		release, err := update.FetchLatest()
		if err != nil {
			return fmt.Errorf("check updates: %w", err)
		}

		latest := update.VersionFromTag(release.TagName)
		if !update.IsNewer(Version, latest) {
			fmt.Printf("Already up to date (v%s).\n", Version)
			return nil
		}

		fmt.Printf("Update available: v%s → v%s\n", Version, latest)

		fmt.Println("Backing up current binary...")
		if err := update.BackupCurrent(); err != nil {
			return fmt.Errorf("backup: %w", err)
		}

		fmt.Println("Downloading new version...")
		if err := update.Download(release, service.BinaryPath()); err != nil {
			return fmt.Errorf("download: %w", err)
		}

		update.ClearRollbackFlag()

		platform, err := service.Detect()
		if err == nil && platform.IsInstalled() {
			fmt.Println("Restarting service...")
			if platform.IsRunning() {
				_ = platform.Stop()
			}
			if err := platform.Start(); err != nil {
				return fmt.Errorf("restart after update: %w", err)
			}
		}

		fmt.Printf("Updated to v%s.\n", latest)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
}
