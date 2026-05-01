package session

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	daemonfs "github.com/gsd-build/daemon/internal/fs"
)

const maxImageUploadBytes = 10 * 1024 * 1024

type imageUploadCandidate struct {
	Path     string
	Filename string
	Data     []byte
}

func imageUploadFromReadPath(filePath string, cwd string, touched map[string]struct{}) (imageUploadCandidate, error) {
	resolved, err := daemonfs.ResolveExistingPathFromCWD(filePath, cwd)
	if err != nil {
		return imageUploadCandidate{}, err
	}
	root, err := daemonfs.ResolveExistingPathFromCWD(cwd, "")
	if err != nil {
		return imageUploadCandidate{}, err
	}
	if !pathWithinRoot(resolved, root) {
		if _, ok := touched[resolved]; !ok {
			return imageUploadCandidate{}, fmt.Errorf("image path %q is outside task root", resolved)
		}
	}
	if strings.EqualFold(filepath.Ext(resolved), ".svg") {
		return imageUploadCandidate{}, fmt.Errorf("svg image upload requires sanitizer")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return imageUploadCandidate{}, err
	}
	if info.Size() > maxImageUploadBytes {
		return imageUploadCandidate{}, fmt.Errorf("image file exceeds %d bytes", maxImageUploadBytes)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return imageUploadCandidate{}, err
	}
	if !sniffsAsSupportedImage(data) {
		return imageUploadCandidate{}, fmt.Errorf("image content type is not supported")
	}
	return imageUploadCandidate{
		Path:     resolved,
		Filename: filepath.Base(resolved),
		Data:     data,
	}, nil
}

func sniffsAsSupportedImage(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return true
	}
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func pathWithinRoot(path, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
