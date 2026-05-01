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

type browseCandidate struct {
	name  string
	path  string
	isDir bool
}

// BrowseDir lists the first default-sized page of entries in the given absolute
// path. Callers that need additional pages should use BrowseDirPageAt.
func BrowseDir(path, scopeRoot string) ([]protocol.BrowseEntry, error) {
	page, err := BrowseDirPageAt(path, scopeRoot, BrowseDirOptions{})
	if err != nil {
		return nil, err
	}
	return page.Entries, nil
}

// BrowseDirPageAt lists one bounded page of entries in the given absolute path.
func BrowseDirPageAt(path, scopeRoot string, opts BrowseDirOptions) (BrowseDirPage, error) {
	resolvedRoot, err := resolveScopeRoot(scopeRoot, false)
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

	candidates := make([]browseCandidate, 0, len(entries))
	for _, e := range entries {
		childPath := filepath.Clean(filepath.Join(resolved, e.Name()))
		if err := ensurePathAllowed(childPath, resolvedRoot); err != nil {
			continue
		}
		candidates = append(candidates, browseCandidate{
			name:  e.Name(),
			path:  childPath,
			isDir: e.IsDir(),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].isDir != candidates[j].isDir {
			return candidates[i].isDir
		}
		return candidates[i].name < candidates[j].name
	})

	limit := normalizeBrowseDirLimit(opts.Limit)
	start, err := parseBrowseCursor(opts.Cursor)
	if err != nil {
		return BrowseDirPage{}, err
	}
	if start >= len(candidates) {
		return BrowseDirPage{Entries: []protocol.BrowseEntry{}}, nil
	}
	end := start + limit
	if end > len(candidates) {
		end = len(candidates)
	}

	result := make([]protocol.BrowseEntry, 0, end-start)
	for _, candidate := range candidates[start:end] {
		info, err := os.Lstat(candidate.path)
		if err != nil {
			continue
		}
		result = append(result, protocol.BrowseEntry{
			Name:        candidate.name,
			Path:        candidate.path,
			IsDirectory: candidate.isDir,
			Size:        info.Size(),
			ModifiedAt:  info.ModTime().UTC().Format(time.RFC3339Nano),
		})
	}
	page := BrowseDirPage{
		Entries: result,
		HasMore: end < len(candidates),
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
