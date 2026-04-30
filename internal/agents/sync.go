package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Definition struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemPrompt string   `json:"systemPrompt"`
	Model        string   `json:"model"`
	Tools        []string `json:"tools"`
}

var safeAgentFileName = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func SyncDefinitions(dir string, defs []Definition) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("agent directory is required")
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("mkdir agents parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, filepath.Base(dir)+".staging-*")
	if err != nil {
		return fmt.Errorf("mkdir agents staging: %w", err)
	}
	stagingActive := true
	defer func() {
		if stagingActive {
			_ = os.RemoveAll(staging)
		}
	}()

	seen := make(map[string]string, len(defs))
	for _, def := range defs {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			return fmt.Errorf("agent name is required")
		}
		def.Name = name
		fileName := safeAgentFileName.ReplaceAllString(name, "_")
		if fileName == "" || fileName == "." || fileName == ".." {
			return fmt.Errorf("invalid agent name %q", name)
		}
		if prior, ok := seen[fileName]; ok {
			return fmt.Errorf("agent names %q and %q resolve to the same file", prior, name)
		}
		seen[fileName] = name
		data, err := json.MarshalIndent(def, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", name, err)
		}
		path := filepath.Join(staging, fileName+".json")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("rename %s: %w", name, err)
		}
	}
	backup := dir + ".previous"
	_ = os.RemoveAll(backup)
	if err := os.Rename(dir, backup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("backup agents dir: %w", err)
	}
	if err := os.Rename(staging, dir); err != nil {
		if _, statErr := os.Stat(backup); statErr == nil {
			_ = os.Rename(backup, dir)
		}
		return fmt.Errorf("replace agents dir: %w", err)
	}
	stagingActive = false
	_ = os.RemoveAll(backup)
	return nil
}

func BuildPrompt(defs []Definition) string {
	if len(defs) == 0 {
		return ""
	}
	sorted := append([]Definition(nil), defs...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	var b strings.Builder
	b.WriteString("<subagents>\n")
	b.WriteString("Use the subagent tool when a focused parallel agent can inspect, review, or debug a bounded task. Available agents:\n")
	for _, def := range sorted {
		b.WriteString("- ")
		b.WriteString(def.Name)
		if def.Model != "" {
			b.WriteString(" [")
			b.WriteString(def.Model)
			b.WriteString("]")
		}
		if len(def.Tools) > 0 {
			b.WriteString(" tools=")
			b.WriteString(strings.Join(def.Tools, ","))
		}
		if def.Description != "" {
			b.WriteString(": ")
			b.WriteString(def.Description)
		}
		b.WriteString("\n")
	}
	b.WriteString("</subagents>")
	return b.String()
}
