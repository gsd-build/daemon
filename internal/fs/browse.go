// Package fs exposes the filesystem operations the daemon allows remote
// browsers to perform. All paths must be absolute; relative paths are rejected
// as a defense against traversal.
package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

// BrowseDir lists entries in the given absolute path.
func BrowseDir(path string) ([]protocol.BrowseEntry, error) {
	// Resolve ~ to user home directory
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}

	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute: %q", path)
	}
	cleaned := filepath.Clean(path)

	// Resolve symlinks so the caller sees the real path.
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return nil, fmt.Errorf("resolve symlinks: %w", err)
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	result := make([]protocol.BrowseEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, protocol.BrowseEntry{
			Name:        e.Name(),
			Path:        filepath.Join(resolved, e.Name()),
			IsDirectory: e.IsDir(),
			Size:        info.Size(),
			ModifiedAt:  info.ModTime().UTC().Format(time.RFC3339Nano),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDirectory != result[j].IsDirectory {
			return result[i].IsDirectory
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
