// Package fs exposes the filesystem operations the daemon allows remote
// browsers to perform. All paths must be absolute; relative paths are rejected
// as a defense against traversal.
package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

// BrowseDir lists directory entries under the given absolute path after validating it against the provided scope root.
// 
// It resolves the scope root and the target path, verifies the target is allowed, and reads the directory contents.
// For each entry it builds a child path, verifies that child path is within the allowed scope, obtains file metadata,
// and collects a protocol.BrowseEntry containing the entry name, child path, whether it is a directory, size, and modification time (UTC, RFC3339Nano).
// Entries that fail per-entry permission checks or metadata retrieval are silently skipped.
// The returned slice is sorted with directories before non-directories and by name ascending.
// If scope or path resolution or the initial permission check fails the call returns an error; a directory read error is wrapped with "read dir:".
func BrowseDir(path, scopeRoot string) ([]protocol.BrowseEntry, error) {
	resolvedRoot, err := resolveScopeRoot(scopeRoot, true)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveExistingPath(path)
	if err != nil {
		return nil, err
	}
	if err := ensurePathAllowed(resolved, resolvedRoot); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	result := make([]protocol.BrowseEntry, 0, len(entries))
	for _, e := range entries {
		childPath := filepath.Clean(filepath.Join(resolved, e.Name()))
		if err := ensurePathAllowed(childPath, resolvedRoot); err != nil {
			continue
		}
		info, err := os.Lstat(childPath)
		if err != nil {
			continue
		}
		result = append(result, protocol.BrowseEntry{
			Name:        e.Name(),
			Path:        childPath,
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
