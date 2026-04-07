package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "0.1.0-dev"

// Commit is the short git SHA, set at build time via -ldflags.
var Commit = "unknown"

// BuildDate is the RFC3339 build timestamp, set at build time via -ldflags.
var BuildDate = "unknown"

func versionString() string {
	return fmt.Sprintf("gsd-cloud %s (commit %s, built %s)", Version, Commit, BuildDate)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the daemon version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(versionString())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
