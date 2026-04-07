package cmd

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "gsd-cloud",
	Short: "GSD Cloud daemon — supervise Claude Code from the cloud",
	Long:  "The GSD Cloud daemon connects your local machine to cloud.gsd.build so you can supervise Claude Code from any browser.",
}

// Execute is the entry point called by main.
func Execute() error {
	return rootCmd.Execute()
}
