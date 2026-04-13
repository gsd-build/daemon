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

// BrowseDir lists entries in the given absolute path.
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
		info, err := e.Info()
		if err != nil {
			continue
		}
		childResolved, err := resolveExistingPath(filepath.Join(resolved, e.Name()))
		if err != nil {
			continue
		}
		if err := ensurePathAllowed(childResolved, resolvedRoot); err != nil {
			continue
		}
		result = append(result, protocol.BrowseEntry{
			Name:        e.Name(),
			Path:        childResolved,
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
