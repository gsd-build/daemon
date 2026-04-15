package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteManagedFile writes a file only when the target is inside an allowed
// managed skill root and is not targeting an installed gsd-* skill.
func WriteManagedFile(path string, managedRoots []string, content []byte) error {
	resolved, _, err := ValidateManagedPath(path, managedRoots)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return fmt.Errorf("mkdir parents: %w", err)
	}
	if err := os.WriteFile(resolved, content, 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// ValidateManagedPath resolves a managed-skill path and verifies that it sits
// inside one of the allowed managed roots while excluding installed gsd-* trees.
func ValidateManagedPath(path string, managedRoots []string) (string, string, error) {
	resolved, err := resolveCreatePath(path)
	if err != nil {
		return "", "", err
	}

	for _, candidate := range managedRoots {
		root, err := resolveCreatePath(candidate)
		if err != nil {
			continue
		}
		if !pathWithinRoot(resolved, root) {
			continue
		}
		if err := ensurePathAllowed(resolved, root); err != nil {
			return "", "", err
		}
		if installedSkillPath(resolved, root) {
			return "", "", fmt.Errorf("installed skill paths are read-only: %q", resolved)
		}
		return resolved, root, nil
	}

	return "", "", fmt.Errorf("path %q is outside managed skill roots", path)
}

func installedSkillPath(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return false
	}
	first := strings.Split(rel, string(filepath.Separator))[0]
	return strings.HasPrefix(first, "gsd-")
}
