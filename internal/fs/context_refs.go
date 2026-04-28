package fs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	protocol "github.com/gsd-build/protocol-go"
)

type ContextRefLimits struct {
	PerFileBytes   int64
	AggregateBytes int64
	FolderEntries  int
}

func DefaultContextRefLimits() ContextRefLimits {
	return ContextRefLimits{
		PerFileBytes:   64 * 1024,
		AggregateBytes: 256 * 1024,
		FolderEntries:  1000,
	}
}

func BuildContextRefBlock(root string, refs []protocol.ContextRef, limits ContextRefLimits) string {
	if len(refs) == 0 {
		return ""
	}

	var b strings.Builder
	var used int64

	b.WriteString("<attached_context>\n")
	scopeRoot, err := resolveScopeRoot(root, false)
	if err != nil {
		fmt.Fprintf(&b, "<skipped kind=%q path=%q reason=%q />\n", "root", ".", err.Error())
		b.WriteString("</attached_context>\n\n")
		return b.String()
	}
	for _, ref := range refs {
		switch ref.Kind {
		case "file":
			writeFileContext(&b, scopeRoot, ref, limits, &used)
		case "folder":
			writeFolderContext(&b, scopeRoot, ref, limits)
		default:
			writeSkippedContext(&b, ref, "unsupported kind")
		}
	}
	b.WriteString("</attached_context>\n\n")

	return b.String()
}

func writeFileContext(b *strings.Builder, root string, ref protocol.ContextRef, limits ContextRefLimits, used *int64) {
	resolved, rel, err := resolveContextRefPath(root, ref.Path)
	if err != nil {
		writeSkippedContext(b, ref, err.Error())
		return
	}

	if isSensitivePath(resolved) {
		writeSkippedContext(b, ref, "sensitive path")
		return
	}

	info, err := os.Stat(resolved)
	if err != nil {
		writeSkippedContext(b, ref, err.Error())
		return
	}
	if info.IsDir() {
		writeSkippedContext(b, ref, "path is a directory")
		return
	}
	if info.Size() > limits.PerFileBytes {
		writeSkippedContext(b, ref, fmt.Sprintf("file is %d bytes; per-file cap is %d bytes", info.Size(), limits.PerFileBytes))
		return
	}
	if *used+info.Size() > limits.AggregateBytes {
		writeSkippedContext(b, ref, fmt.Sprintf("aggregate cap is %d bytes", limits.AggregateBytes))
		return
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		writeSkippedContext(b, ref, err.Error())
		return
	}
	if bytes.Contains(data, []byte{0}) || !utf8.Valid(data) {
		writeSkippedContext(b, ref, "binary file")
		return
	}

	*used += int64(len(data))
	fmt.Fprintf(b, "<file path=%q bytes=%d>\n", rel, len(data))
	b.Write(data)
	if len(data) == 0 || data[len(data)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("</file>\n")
}

func writeFolderContext(b *strings.Builder, root string, ref protocol.ContextRef, limits ContextRefLimits) {
	resolved, rel, err := resolveContextRefPath(root, ref.Path)
	if err != nil {
		writeSkippedContext(b, ref, err.Error())
		return
	}
	if isSensitivePath(resolved) {
		writeSkippedContext(b, ref, "sensitive path")
		return
	}

	info, err := os.Stat(resolved)
	if err != nil {
		writeSkippedContext(b, ref, err.Error())
		return
	}
	if !info.IsDir() {
		writeSkippedContext(b, ref, "path is a file")
		return
	}

	entries := make([]string, 0, limits.FolderEntries)
	truncated := false
	walkContextFolder(resolved, rel, limits.FolderEntries, &entries, &truncated)

	fmt.Fprintf(b, "<folder path=%q entries=%d truncated=%t>\n", rel, len(entries), truncated)
	for _, entry := range entries {
		b.WriteString(entry)
		b.WriteByte('\n')
	}
	b.WriteString("</folder>\n")
}

func walkContextFolder(root string, relRoot string, limit int, entries *[]string, truncated *bool) {
	if limit <= 0 {
		*truncated = true
		return
	}

	children, err := os.ReadDir(root)
	if err != nil {
		return
	}
	sort.Slice(children, func(i, j int) bool {
		return children[i].Name() < children[j].Name()
	})

	for _, child := range children {
		if len(*entries) >= limit {
			*truncated = true
			return
		}

		childPath := filepath.Join(root, child.Name())
		if isSensitivePath(childPath) {
			continue
		}
		childRel := filepath.ToSlash(filepath.Join(relRoot, child.Name()))
		if child.IsDir() {
			*entries = append(*entries, childRel+"/")
			walkContextFolder(childPath, childRel, limit, entries, truncated)
			if *truncated {
				return
			}
			continue
		}
		*entries = append(*entries, childRel)
	}
}

func resolveContextRefPath(root string, rel string) (string, string, error) {
	if filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("absolute paths are not accepted")
	}

	clean := filepath.Clean(rel)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", "", fmt.Errorf("path is outside project root")
	}

	candidate := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve symlinks: %w", err)
	}
	if !pathWithinRoot(resolved, root) {
		return "", "", fmt.Errorf("path is outside project root")
	}

	return resolved, filepath.ToSlash(clean), nil
}

func writeSkippedContext(b *strings.Builder, ref protocol.ContextRef, reason string) {
	fmt.Fprintf(b, "<skipped kind=%q path=%q reason=%q />\n", ref.Kind, ref.Path, reason)
}
