package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gsd-build/daemon/internal/service"
	"github.com/spf13/cobra"
)

type logsOptionsState struct {
	sessionID string
	taskID    string
	lastTask  bool
	since     time.Duration
	level     string
	pretty    bool
	json      bool
	color     string
	noColor   bool
}

var logsOptions logsOptionsState

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
		if err := validateLogsOptions(); err != nil {
			return err
		}

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

		if !logsOptions.hasStructuredMode() {
			return streamLogFile(ctx, logPath, cmd.OutOrStdout(), 250*time.Millisecond)
		}

		return renderLogFile(logPath, cmd.OutOrStdout())
	},
}

func (opts logsOptionsState) hasStructuredMode() bool {
	return opts.sessionID != "" ||
		opts.taskID != "" ||
		opts.lastTask ||
		opts.since > 0 ||
		opts.level != "" ||
		opts.pretty ||
		opts.json ||
		opts.color != "auto" ||
		opts.noColor
}

func validateLogsOptions() error {
	count := 0
	if logsOptions.sessionID != "" {
		count++
	}
	if logsOptions.taskID != "" {
		count++
	}
	if logsOptions.lastTask {
		count++
	}
	if count > 1 {
		return fmt.Errorf("--session, --task, and --last-task are mutually exclusive")
	}
	if logsOptions.pretty && logsOptions.json {
		return fmt.Errorf("--pretty and --json are mutually exclusive")
	}
	switch logsOptions.color {
	case string(colorAuto), string(colorAlways), string(colorNever):
	default:
		return fmt.Errorf("--color must be auto, always, or never")
	}
	return nil
}

func renderLogFile(logPath string, out io.Writer) error {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	filter := logFilter{
		TaskID:    logsOptions.taskID,
		SessionID: logsOptions.sessionID,
		Since:     logsOptions.since,
		Level:     logsOptions.level,
	}
	if logsOptions.lastTask {
		filter.TaskID = latestTaskID(lines)
	}
	events := filterLogLines(lines, filter)
	if logsOptions.json {
		for _, event := range events {
			if event.raw == "" {
				continue
			}
			if _, err := fmt.Fprintln(out, event.raw); err != nil {
				return err
			}
		}
		return nil
	}
	mode := colorMode(logsOptions.color)
	if logsOptions.noColor {
		mode = colorNever
	}
	_, err = io.WriteString(out, renderPrettyTimeline(events, mode))
	return err
}

func latestTaskID(lines []string) string {
	events := filterLogLines(lines, logFilter{})
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].TaskID != "" {
			return events[i].TaskID
		}
	}
	return ""
}

func init() {
	logsCmd.Flags().StringVar(&logsOptions.sessionID, "session", "", "show log events for one session")
	logsCmd.Flags().StringVar(&logsOptions.taskID, "task", "", "show log events for one task")
	logsCmd.Flags().BoolVar(&logsOptions.lastTask, "last-task", false, "show the latest known task in local logs")
	logsCmd.Flags().DurationVar(&logsOptions.since, "since", 0, "limit logs by duration, for example 10m or 2h")
	logsCmd.Flags().StringVar(&logsOptions.level, "level", "", "minimum level: debug, info, warn, error")
	logsCmd.Flags().BoolVar(&logsOptions.pretty, "pretty", false, "render a human timeline")
	logsCmd.Flags().BoolVar(&logsOptions.json, "json", false, "emit filtered structured JSON lines")
	logsCmd.Flags().StringVar(&logsOptions.color, "color", "auto", "color mode: auto, always, never")
	logsCmd.Flags().BoolVar(&logsOptions.noColor, "no-color", false, "disable color output")
	rootCmd.AddCommand(logsCmd)
}
