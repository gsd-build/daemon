package pidfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Dir returns the PID file directory: ~/.gsd-cloud/pids/
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".gsd-cloud", "pids"), nil
}

// Write creates a PID file at the given path.
func Write(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0600)
}

// Read returns the PID stored in the file.
func Read(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
	}
	return pid, nil
}

// Remove deletes the PID file. No error if it doesn't exist.
func Remove(path string) {
	_ = os.Remove(path)
}

// CleanStale removes PID files in dir whose process is no longer running.
// Returns the number of files cleaned.
func CleanStale(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	cleaned := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		pid, err := Read(path)
		if err != nil {
			Remove(path)
			cleaned++
			continue
		}
		if !processRunning(pid) {
			Remove(path)
			cleaned++
		}
	}
	return cleaned
}

// processRunning checks if a process with the given PID exists.
func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
