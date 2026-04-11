// Package logging configures structured logging via log/slog with optional
// log-file rotation via lumberjack.
package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Mode selects logging output.
type Mode int

const (
	// ModeForeground writes human-readable text to stderr.
	ModeForeground Mode = iota
	// ModeService writes JSON to a rotated log file.
	ModeService
)

// DefaultLogPath returns the standard daemon log file location.
func DefaultLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gsd-cloud", "logs", "daemon.log")
}

// ParseLevel maps a string to an slog.Level. Unrecognised values map to
// LevelInfo.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Setup creates and returns a configured *slog.Logger and sets it as the
// slog default.
//
// ModeForeground produces a TextHandler writing to stderr.
// ModeService produces a JSONHandler writing to a lumberjack-rotated log file.
// If the log directory cannot be created in ModeService, it falls back to
// JSON on stderr.
func Setup(mode Mode, levelStr string, logPath string) *slog.Logger {
	level := ParseLevel(levelStr)
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler

	switch mode {
	case ModeService:
		if logPath == "" {
			logPath = DefaultLogPath()
		}

		dir := filepath.Dir(logPath)
		if err := os.MkdirAll(dir, 0700); err != nil {
			// Fall back to stderr JSON.
			handler = slog.NewJSONHandler(os.Stderr, opts)
			break
		}

		lj := &lumberjack.Logger{
			Filename:   logPath,
			MaxSize:    10, // megabytes
			MaxBackups: 3,
			Compress:   false,
		}
		handler = slog.NewJSONHandler(lj, opts)

	default: // ModeForeground
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}
