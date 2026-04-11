package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/logging"
	"github.com/gsd-build/daemon/internal/loop"
	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

var (
	flagForeground bool
	flagService    bool
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon and connect to GSD Cloud",
	Long: `Start the GSD Cloud daemon.

Without flags: ensures the background service is installed and running.
With --foreground: runs in the current terminal with human-readable output.
With --service: runs with JSON structured logging (used by the service manager).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !flagForeground && !flagService {
			return startViaService()
		}
		return startDaemon()
	},
}

func init() {
	startCmd.Flags().BoolVar(&flagForeground, "foreground", false, "Run in the current terminal with live output")
	startCmd.Flags().BoolVar(&flagService, "service", false, "Run with JSON logging (used by service manager)")
	rootCmd.AddCommand(startCmd)
}

func startViaService() error {
	platform, err := service.Detect()
	if err != nil {
		return err
	}
	if !platform.IsInstalled() {
		fmt.Println("Service not installed. Installing...")
		if err := platform.Install(); err != nil {
			return fmt.Errorf("install: %w", err)
		}
		fmt.Println("Service installed and started.")
		fmt.Println("The daemon will start automatically on login.")
		return nil
	}
	if platform.IsRunning() {
		fmt.Println("Daemon is already running.")
		return nil
	}
	fmt.Println("Starting daemon...")
	if err := platform.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	fmt.Println("Daemon started.")
	return nil
}

func startDaemon() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("not paired — run 'gsd-cloud login' first: %w", err)
	}

	var logMode logging.Mode
	if flagService {
		logMode = logging.ModeService
	} else {
		logMode = logging.ModeForeground
	}
	logging.Setup(logMode, cfg.LogLevel, "")

	// Write boot marker for crash detection
	if flagService {
		marker := service.BootMarkerPath()
		_ = os.MkdirAll(filepath.Dir(marker), 0700)
		_ = os.WriteFile(marker, []byte(Version), 0600)
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
		sig := <-sigCh
		slog.Info("received signal, starting graceful shutdown", "signal", sig.String())
		cancel()
	}()

	slog.Info("daemon starting",
		"version", Version,
		"relay", cfg.RelayURL,
		"machine", cfg.MachineID,
	)

	if err := d.Run(ctx); err != nil && err != context.Canceled {
		return err
	}

	slog.Info("daemon stopped")
	return nil
}
