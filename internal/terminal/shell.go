package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func ResolveShell() string {
	if shell := os.Getenv("SHELL"); isExecutableAbs(shell) {
		return shell
	}
	if runtime.GOOS == "darwin" && isExecutableAbs("/bin/zsh") {
		return "/bin/zsh"
	}
	if isExecutableAbs("/bin/bash") {
		return "/bin/bash"
	}
	return "/bin/sh"
}

func ValidateCWD(cwd string) (string, error) {
	if cwd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		cwd = home
	}
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("cwd must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(cwd))
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat cwd: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd is not a directory")
	}
	return resolved, nil
}

func isExecutableAbs(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0
}
