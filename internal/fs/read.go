package fs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DefaultMaxBytes is the default file size cap.
const DefaultMaxBytes = 512 * 1024

// ReadFile returns up to maxBytes of file content and whether it was truncated.
func ReadFile(path string, maxBytes int) (string, bool, error) {
	if !filepath.IsAbs(path) {
		return "", false, fmt.Errorf("path must be absolute: %q", path)
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", false, fmt.Errorf("resolve symlinks: %w", err)
	}

	f, err := os.Open(resolved)
	if err != nil {
		return "", false, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	buf := make([]byte, maxBytes+1) // read one extra byte to detect truncation
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", false, fmt.Errorf("read: %w", err)
	}

	if n > maxBytes {
		return string(buf[:maxBytes]), true, nil
	}
	return string(buf[:n]), false, nil
}
