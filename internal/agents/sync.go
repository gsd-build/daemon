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
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir agents dir: %w", err)
	}

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
		data, err := json.MarshalIndent(def, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", name, err)
		}
		path := filepath.Join(dir, fileName+".json")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("rename %s: %w", name, err)
		}
	}
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
