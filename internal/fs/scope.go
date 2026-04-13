package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveScopeRoot(scopeRoot string, allowHomeFallback bool) (string, error) {
	if scopeRoot == "" {
		if !allowHomeFallback {
			return "", fmt.Errorf("project root is unavailable for this request")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		scopeRoot = home
	}

	cleaned, err := resolveInputPath(scopeRoot)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve scope root: %w", err)
	}
	return resolved, nil
}

func resolveInputPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}

	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute: %q", path)
	}

	return filepath.Clean(path), nil
}

func resolveExistingPath(path string) (string, error) {
	cleaned, err := resolveInputPath(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	return resolved, nil
}

func resolveCreatePath(path string) (string, error) {
	cleaned, err := resolveInputPath(path)
	if err != nil {
		return "", err
	}

	current := cleaned
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			full := resolved
			for i := len(suffix) - 1; i >= 0; i-- {
				full = filepath.Join(full, suffix[i])
			}
			return filepath.Clean(full), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve symlinks: %w", err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve symlinks: %w", err)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func ensurePathAllowed(resolvedPath, scopeRoot string) error {
	if !pathWithinRoot(resolvedPath, scopeRoot) {
		return fmt.Errorf("path %q is outside allowed root %q", resolvedPath, scopeRoot)
	}
	if isSensitivePath(resolvedPath) {
		return fmt.Errorf("path %q is blocked", resolvedPath)
	}
	return nil
}

func pathWithinRoot(path, root string) bool {
	if path == root {
		return true
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isSensitivePath(path string) bool {
	cleaned := filepath.Clean(path)
	if cleaned == "/etc/shadow" {
		return true
	}

	parts := strings.Split(cleaned, string(filepath.Separator))
	for _, part := range parts {
		switch part {
		case ".ssh", ".aws", ".gnupg", ".config":
			return true
		}
	}
	return false
}
