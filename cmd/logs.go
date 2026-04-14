package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

func streamLogFile(ctx context.Context, logPath string, out io.Writer, pollInterval time.Duration) error {
	fh, err := os.Open(logPath)
	if err != nil {
		return err
	}
	defer fh.Close()

	offset, err := io.Copy(out, fh)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			stat, err := os.Stat(logPath)
			if err != nil {
				return err
			}

			if stat.Size() < offset {
				if _, err := fh.Seek(0, io.SeekStart); err != nil {
					return err
				}
				offset = 0
			}

			if stat.Size() == offset {
				continue
			}

			if _, err := fh.Seek(offset, io.SeekStart); err != nil {
				return err
			}
			written, err := io.Copy(out, fh)
			if err != nil {
				return err
			}
			offset += written
		}
	}
}

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Tail the daemon log file",
	RunE: func(cmd *cobra.Command, args []string) error {
		logPath := service.LogPath()
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			return fmt.Errorf("no log file at %s — is the daemon installed?", logPath)
		}

		parent := cmd.Context()
		if parent == nil {
			parent = context.Background()
		}
		ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
		defer stop()

		return streamLogFile(ctx, logPath, os.Stdout, 250*time.Millisecond)
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
}
