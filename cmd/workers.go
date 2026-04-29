package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gsd-build/daemon/internal/sockapi"
	"github.com/spf13/cobra"
)

var workersCmd = &cobra.Command{
	Use:   "workers",
	Short: "Show warm provider workers",
	RunE: func(cmd *cobra.Command, args []string) error {
		sockPath, err := socketPath()
		if err != nil {
			return fmt.Errorf("socket path: %w", err)
		}
		if !sockapi.IsDaemonRunning(sockPath) {
			fmt.Println("Daemon is not running.")
			fmt.Println("Run 'gsd-cloud start' to connect.")
			return nil
		}

		status, err := sockapi.QueryStatus(sockPath)
		if err != nil {
			return err
		}
		workers, err := sockapi.QueryWorkers(sockPath)
		if err != nil {
			return err
		}
		fmt.Print(formatWorkers(status, workers, time.Now()))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(workersCmd)
}

func formatWorkers(status *sockapi.StatusData, workers []sockapi.WorkerInfo, now time.Time) string {
	var b strings.Builder
	if status == nil {
		status = &sockapi.StatusData{}
	}

	fmt.Fprintf(&b, "warm workers: %s", onOff(status.WarmWorkersEnabled))
	if status.WarmWorkerIdleTTL != "" || status.WarmWorkerIdleCap != 0 || status.ActiveWarmWorkers != 0 || status.IdleWarmWorkers != 0 {
		fmt.Fprintf(&b, " (%d active, %d idle, ttl %s, cap %d)",
			status.ActiveWarmWorkers,
			status.IdleWarmWorkers,
			status.WarmWorkerIdleTTL,
			status.WarmWorkerIdleCap,
		)
	}
	b.WriteByte('\n')

	if len(workers) == 0 {
		b.WriteString("workers: none\n")
		return b.String()
	}

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tSTATE\tPROVIDER\tMODEL\tPID\tIDLE")
	for _, worker := range workers {
		idleFor := "-"
		if worker.IdleSince != nil {
			idleFor = now.Sub(*worker.IdleSince).Truncate(time.Second).String()
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n",
			worker.SessionID,
			worker.State,
			worker.Provider,
			valueOrDefault(worker.Model, "default"),
			worker.PID,
			idleFor,
		)
	}
	_ = tw.Flush()
	return b.String()
}

func valueOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
