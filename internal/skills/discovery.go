package skills

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	protocol "github.com/gsd-build/protocol-go"
)

const (
	maxAncestorDirs     = 64
	maxDiscoveredSkills = 500
	maxSkillReadBytes   = 16 * 1024
)

type root struct {
	path  string
	scope string
}

// DiscoverClaudeSkills returns bounded metadata for Claude-compatible skill
// roots. Project roots are checked by walking from cwd upward and reading only
// direct children of each .claude/skills directory.
func DiscoverClaudeSkills(cwd string) ([]protocol.Skill, error) {
	roots, err := claudeSkillRoots(cwd)
	if err != nil {
		return nil, err
	}

	seenNames := make(map[string]struct{})
	seenPaths := make(map[string]struct{})
	out := make([]protocol.Skill, 0)
	for _, root := range roots {
		skills, err := skillsInRoot(root)
		if err != nil {
			continue
		}
		for _, skill := range skills {
			if _, ok := seenPaths[skill.Path]; ok {
				continue
			}
			if _, ok := seenNames[skill.Name]; ok {
				continue
			}
			seenPaths[skill.Path] = struct{}{}
			seenNames[skill.Name] = struct{}{}
			out = append(out, skill)
			if len(out) >= maxDiscoveredSkills {
				return out, nil
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope == "project"
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func claudeSkillRoots(cwd string) ([]root, error) {
	roots := make([]root, 0)
	seen := make(map[string]struct{})
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	home = filepath.Clean(home)

	if cwd != "" {
		current, err := filepath.Abs(cwd)
		if err != nil {
			return nil, fmt.Errorf("resolve cwd: %w", err)
		}
		current = filepath.Clean(current)
		for i := 0; i < maxAncestorDirs; i++ {
			if current != home {
				addRoot(&roots, seen, filepath.Join(current, ".claude", "skills"), "project")
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}

	addRoot(&roots, seen, filepath.Join(home, ".claude", "skills"), "home")

	return roots, nil
}

func addRoot(roots *[]root, seen map[string]struct{}, path string, scope string) {
	cleaned := filepath.Clean(path)
	if _, ok := seen[cleaned]; ok {
		return
	}
	seen[cleaned] = struct{}{}
	*roots = append(*roots, root{path: cleaned, scope: scope})
}

func skillsInRoot(root root) ([]protocol.Skill, error) {
	entries, err := os.ReadDir(root.path)
	if err != nil {
		return nil, err
	}

	out := make([]protocol.Skill, 0, len(entries))
	if skill, ok := readSkill(filepath.Join(root.path, "SKILL.md"), filepath.Base(root.path), root.scope); ok {
		out = append(out, skill)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(root.path, entry.Name(), "SKILL.md")
		if skill, ok := readSkill(skillPath, entry.Name(), root.scope); ok {
			out = append(out, skill)
		}
	}
	return out, nil
}

func readSkill(path string, fallbackName string, scope string) (protocol.Skill, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return protocol.Skill{}, false
	}

	file, err := os.Open(path)
	if err != nil {
		return protocol.Skill{}, false
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Debug("close skill metadata file failed", "path", path, "err", err)
		}
	}()

	name, description := parseSkillMetadata(bufio.NewScanner(io.LimitReader(file, maxSkillReadBytes)))
	if name == "" {
		name = fallbackName
	}
	return protocol.Skill{
		Name:        name,
		Description: description,
		Path:        filepath.Clean(path),
		Scope:       scope,
	}, true
}

func parseSkillMetadata(scanner *bufio.Scanner) (string, string) {
	scanner.Buffer(make([]byte, 1024), maxSkillReadBytes)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return "", ""
	}

	name := ""
	description := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	return name, description
}
