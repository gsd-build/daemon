package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MkDir creates a directory at the given absolute path.
// Parent directories are created as needed (like mkdir -p).
func MkDir(path string) error {
	// Resolve ~ to user home directory
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home dir: %w", err)
		}
		if path == "~" {
			return fmt.Errorf("cannot mkdir home directory itself")
		}
		path = filepath.Join(home, path[2:])
	}

	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute: %q", path)
	}
	cleaned := filepath.Clean(path)

	return os.MkdirAll(cleaned, 0755)
}
