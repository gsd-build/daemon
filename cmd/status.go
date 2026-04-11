package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/sockapi"
	"github.com/spf13/cobra"
)

// socketPath returns the path to the daemon Unix socket.
func socketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gsd-cloud", "daemon.sock"), nil
}

// formatLiveStatus formats output when the daemon is running and reachable.
func formatLiveStatus(s *sockapi.StatusData, sessions []sockapi.SessionInfo) string {
	var b strings.Builder

	statusLabel := "connected"
	if !s.RelayConnected {
		statusLabel = "disconnected (relay unreachable)"
	}

	executing := 0
	idle := 0
	for _, sess := range sessions {
		if sess.State == "executing" {
			executing++
		} else {
			idle++
		}
	}

	fmt.Fprintf(&b, "gsd-cloud v%s\n", s.Version)
	fmt.Fprintf(&b, "status:     %s\n", statusLabel)
	fmt.Fprintf(&b, "relay:      %s\n", s.RelayURL)
	fmt.Fprintf(&b, "uptime:     %s\n", s.Uptime)
	fmt.Fprintf(&b, "sessions:   %d active (%d executing, %d idle)\n", s.ActiveSessions, executing, idle)
	fmt.Fprintf(&b, "tasks:      %d in-flight (max %d)\n", s.InFlightTasks, s.MaxConcurrentTasks)

	return b.String()
}

// formatStaticStatus formats output when the daemon is not running.
func formatStaticStatus(version, machineID, relayURL string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "gsd-cloud v%s\n", version)
	fmt.Fprintf(&b, "status:     not running\n")
	fmt.Fprintf(&b, "machine:    %s\n", machineID)
	fmt.Fprintf(&b, "relay:      %s\n", relayURL)
	fmt.Fprintf(&b, "\nRun 'gsd-cloud start' to connect.\n")

	return b.String()
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		sockPath, err := socketPath()
		if err != nil {
			return fmt.Errorf("socket path: %w", err)
		}

		// Try live daemon first.
		if sockapi.IsDaemonRunning(sockPath) {
			status, err := sockapi.QueryStatus(sockPath)
			if err == nil {
				sessions, _ := sockapi.QuerySessions(sockPath)
				if sessions == nil {
					sessions = []sockapi.SessionInfo{}
				}
				fmt.Print(formatLiveStatus(status, sessions))
				return nil
			}
			// Fall through to static if query failed despite socket existing.
		}

		// Daemon not running — show static config.
		cfg, err := config.Load()
		if err != nil {
			fmt.Println("Not paired.")
			fmt.Println("Run 'gsd-cloud login <code>' to pair.")
			return nil
		}

		fmt.Print(formatStaticStatus(Version, cfg.MachineID, cfg.RelayURL))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
