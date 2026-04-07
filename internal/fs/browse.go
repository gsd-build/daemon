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

	protocol "github.com/gsd-cloud/protocol-go"
)

// BrowseDir lists entries in the given absolute path.
func BrowseDir(path string) ([]protocol.BrowseEntry, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute: %q", path)
	}
	cleaned := filepath.Clean(path)

	entries, err := os.ReadDir(cleaned)
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
			Path:        filepath.Join(cleaned, e.Name()),
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
