package cmd

import (
	"fmt"
	"strings"
	"time"
)

type colorMode string

const (
	colorAuto   colorMode = "auto"
	colorAlways colorMode = "always"
	colorNever  colorMode = "never"
)

func renderPrettyTimeline(events []logEvent, mode colorMode) string {
	var b strings.Builder
	for _, event := range events {
		label := prettyPhase(event.Phase)
		ts := prettyTime(event.Time)
		line := fmt.Sprintf("%s  %-18s", ts, label)
		if event.TaskID != "" && event.Phase == "task_received" {
			line += "  task=" + shortID(event.TaskID)
		}
		if event.PID != 0 {
			line += fmt.Sprintf("  pid=%d", event.PID)
		}
		if event.Model != "" {
			line += "  model=" + event.Model
		}
		if event.FailureCode != "" {
			line += "  " + event.FailureCode
		}
		if event.Retryable {
			line += "  retryable"
		}
		if event.ElapsedMs > 0 {
			line += fmt.Sprintf("  elapsed=%.1fs", float64(event.ElapsedMs)/1000)
		}
		if event.PromptPreview != "" {
			line += `  "` + event.PromptPreview + `"`
		}
		if event.Cleanup != "" {
			line += "  " + event.Cleanup
		}
		if mode != colorNever {
			line = colorizePrettyLine(line, event.Phase)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func prettyTime(raw string) string {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "--:--:--"
	}
	return parsed.Format("15:04:05")
}

func prettyPhase(phase string) string {
	switch phase {
	case "task_received":
		return "task received"
	case "pi_process_started":
		return "pi started"
	case "prompt_written":
		return "prompt written"
	case "first_event_seen":
		return "first event seen"
	case "first_visible_event_seen":
		return "first visible event"
	case "cleanup_started":
		return "cleanup started"
	case "cleanup_finished":
		return "cleanup finished"
	case "task_completed":
		return "completed"
	case "task_failed":
		return "failed"
	case "timed_out", "task_timed_out":
		return "timed out"
	default:
		return strings.ReplaceAll(phase, "_", " ")
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func colorizePrettyLine(line string, phase string) string {
	switch phase {
	case "task_completed", "cleanup_finished":
		return "\x1b[32m" + line + "\x1b[0m"
	case "task_failed", "task_timed_out", "timed_out", "task_lost":
		return "\x1b[31m" + line + "\x1b[0m"
	case "waiting_input", "retry_scheduled":
		return "\x1b[33m" + line + "\x1b[0m"
	case "pi_process_started", "prompt_written", "first_event_seen", "first_visible_event_seen":
		return "\x1b[36m" + line + "\x1b[0m"
	default:
		return line
	}
}
