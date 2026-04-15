package fs

import (
	"fmt"
	"os"
)

// RemoveManagedPath removes a file or directory tree only when it is inside an
// allowed managed skill root and is not targeting an installed gsd-* skill.
func RemoveManagedPath(path string, managedRoots []string) error {
	resolved, root, err := ValidateManagedPath(path, managedRoots)
	if err != nil {
		return err
	}
	if resolved == root {
		return fmt.Errorf("refusing to remove managed root %q", resolved)
	}
	if err := os.RemoveAll(resolved); err != nil {
		return fmt.Errorf("remove path: %w", err)
	}
	return nil
}
