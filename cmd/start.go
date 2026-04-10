package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/display"
	"github.com/gsd-build/daemon/internal/loop"
	"github.com/spf13/cobra"
)

var (
	flagQuiet bool
	flagDebug bool
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon and connect to GSD Cloud",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("not paired — run `gsd-cloud login` first: %w", err)
		}

		verbosity := display.Default
		if flagDebug {
			verbosity = display.Debug
		} else if flagQuiet {
			verbosity = display.Quiet
		}

		d, err := loop.New(cfg, Version, verbosity)
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
	startCmd.Flags().BoolVar(&flagQuiet, "quiet", false, "Connection status and heartbeat only")
	startCmd.Flags().BoolVar(&flagDebug, "debug", false, "Full event details (thinking, tool results, inputs)")
	rootCmd.AddCommand(startCmd)
}
