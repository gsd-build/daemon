package fs

import "os"

// MkDir creates a directory at the given absolute path.
// Parent directories are created as needed (like mkdir -p).
func MkDir(path, scopeRoot string) error {
	resolvedRoot, err := resolveScopeRoot(scopeRoot, true)
	if err != nil {
		return err
	}
	resolved, err := resolveCreatePath(path)
	if err != nil {
		return err
	}
	if err := ensurePathAllowed(resolved, resolvedRoot); err != nil {
		return err
	}
	return os.MkdirAll(resolved, 0755)
}
