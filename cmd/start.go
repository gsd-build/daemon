package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/loop"
	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon and connect to GSD Cloud",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("not paired — run `gsd-cloud login` first: %w", err)
		}

		d, err := loop.New(cfg, Version)
		if err != nil {
			return fmt.Errorf("init daemon: %w", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			fmt.Println("\nShutting down...")
			cancel()
		}()

		fmt.Printf("Connecting to %s as %s...\n", cfg.RelayURL, cfg.MachineID)
		if err := d.Run(ctx); err != nil && err != context.Canceled {
			return err
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
}
