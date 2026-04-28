// Package fs exposes the filesystem operations the daemon allows remote
// browsers to perform. All paths must be absolute; relative paths are rejected
// as a defense against traversal.
package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

const (
	defaultBrowseDirLimit = 200
	maxBrowseDirLimit     = 500
)

type BrowseDirOptions struct {
	Limit  int
	Cursor string
}

type BrowseDirPage struct {
	Entries    []protocol.BrowseEntry
	HasMore    bool
	NextCursor string
}

// BrowseDir lists entries in the given absolute path.
func BrowseDir(path, scopeRoot string) ([]protocol.BrowseEntry, error) {
	page, err := BrowseDirPageAt(path, scopeRoot, BrowseDirOptions{})
	if err != nil {
		return nil, err
	}
	return page.Entries, nil
}

// BrowseDirPageAt lists one bounded page of entries in the given absolute path.
func BrowseDirPageAt(path, scopeRoot string, opts BrowseDirOptions) (BrowseDirPage, error) {
	resolvedRoot, err := resolveScopeRoot(scopeRoot, true)
	if err != nil {
		return BrowseDirPage{}, err
	}
	resolved, err := resolveExistingPath(path)
	if err != nil {
		return BrowseDirPage{}, err
	}
	if err := ensurePathAllowed(resolved, resolvedRoot); err != nil {
		return BrowseDirPage{}, err
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return BrowseDirPage{}, fmt.Errorf("read dir: %w", err)
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

	limit := normalizeBrowseDirLimit(opts.Limit)
	start, err := parseBrowseCursor(opts.Cursor)
	if err != nil {
		return BrowseDirPage{}, err
	}
	if start >= len(result) {
		return BrowseDirPage{Entries: []protocol.BrowseEntry{}}, nil
	}
	end := start + limit
	if end > len(result) {
		end = len(result)
	}
	page := BrowseDirPage{
		Entries: result[start:end],
		HasMore: end < len(result),
	}
	if page.HasMore {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func normalizeBrowseDirLimit(limit int) int {
	if limit <= 0 {
		return defaultBrowseDirLimit
	}
	if limit > maxBrowseDirLimit {
		return maxBrowseDirLimit
	}
	return limit
}

func parseBrowseCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	start, err := strconv.Atoi(cursor)
	if err != nil || start < 0 {
		return 0, fmt.Errorf("invalid browse cursor")
	}
	return start, nil
}
