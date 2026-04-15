package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	protocol "github.com/gsd-build/protocol-go"
)

type DiscoverOptions struct {
	HomeDir      string
	ProjectRoots []string
}

type rootDef struct {
	root        string
	projectRoot string
	scope       string
	runtime     string
	sourceKind  string
	editable    bool
	legacyCmds  bool
}

func ManagedRoots(homeDir string) []string {
	return []string{
		filepath.Join(homeDir, ".claude", "skills"),
		filepath.Join(homeDir, ".codex", "skills"),
	}
}

func ProjectSkillRoots(projectRoot string) []string {
	return []string{
		filepath.Join(projectRoot, ".claude", "skills"),
		filepath.Join(projectRoot, ".agents", "skills"),
		filepath.Join(projectRoot, ".cursor", "skills"),
		filepath.Join(projectRoot, ".github", "skills"),
		filepath.Join(projectRoot, ".codex", "skills"),
	}
}

func Discover(opts DiscoverOptions) ([]protocol.SkillInventoryEntry, error) {
	homeDir := opts.HomeDir
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
	}

	var roots []rootDef
	for _, projectRoot := range opts.ProjectRoots {
		roots = append(roots,
			rootDef{root: filepath.Join(projectRoot, ".claude", "skills"), projectRoot: projectRoot, scope: "project", runtime: "claude", sourceKind: "skill_dir"},
			rootDef{root: filepath.Join(projectRoot, ".agents", "skills"), projectRoot: projectRoot, scope: "project", runtime: "agents", sourceKind: "skill_dir"},
			rootDef{root: filepath.Join(projectRoot, ".cursor", "skills"), projectRoot: projectRoot, scope: "project", runtime: "cursor", sourceKind: "skill_dir"},
			rootDef{root: filepath.Join(projectRoot, ".github", "skills"), projectRoot: projectRoot, scope: "project", runtime: "github", sourceKind: "skill_dir"},
			rootDef{root: filepath.Join(projectRoot, ".codex", "skills"), projectRoot: projectRoot, scope: "project", runtime: "codex", sourceKind: "skill_dir"},
		)
	}

	roots = append(roots,
		rootDef{root: filepath.Join(homeDir, ".claude", "skills"), scope: "global", runtime: "claude", sourceKind: "skill_dir", editable: true},
		rootDef{root: filepath.Join(homeDir, ".codex", "skills"), scope: "global", runtime: "codex", sourceKind: "skill_dir", editable: true},
		rootDef{root: filepath.Join(homeDir, ".claude", "get-shit-done", "skills"), scope: "global", runtime: "claude", sourceKind: "skill_dir"},
		rootDef{root: filepath.Join(homeDir, ".claude", "commands", "gsd"), scope: "installed", runtime: "claude", sourceKind: "legacy_command", legacyCmds: true},
	)

	var entries []protocol.SkillInventoryEntry
	for _, root := range roots {
		if root.legacyCmds {
			legacy, err := discoverLegacyCommands(root)
			if err != nil {
				return nil, err
			}
			entries = append(entries, legacy...)
			continue
		}

		skills, err := discoverSkillDirs(root)
		if err != nil {
			return nil, err
		}
		entries = append(entries, skills...)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Slug != entries[j].Slug {
			return entries[i].Slug < entries[j].Slug
		}
		if entries[i].Root != entries[j].Root {
			return entries[i].Root < entries[j].Root
		}
		return entries[i].RelativePath < entries[j].RelativePath
	})

	return entries, nil
}

func discoverSkillDirs(root rootDef) ([]protocol.SkillInventoryEntry, error) {
	info, err := os.Stat(root.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat skill root %q: %w", root.root, err)
	}
	if !info.IsDir() {
		return nil, nil
	}

	dirEntries, err := os.ReadDir(root.root)
	if err != nil {
		return nil, fmt.Errorf("read skill root %q: %w", root.root, err)
	}

	out := make([]protocol.SkillInventoryEntry, 0, len(dirEntries))
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(root.root, entry.Name(), "SKILL.md")
		content, err := os.ReadFile(skillPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read skill file %q: %w", skillPath, err)
		}

		name, description := parseSkillMetadata(entry.Name(), string(content))
		scope := root.scope
		editable := root.editable
		if root.scope == "global" && strings.HasPrefix(entry.Name(), "gsd-") {
			scope = "installed"
			editable = false
		}
		fp, err := skillDirFingerprint(filepath.Join(root.root, entry.Name()))
		if err != nil {
			return nil, err
		}

		out = append(out, protocol.SkillInventoryEntry{
			Slug:               entry.Name(),
			DisplayName:        name,
			Description:        description,
			Scope:              scope,
			Runtime:            root.runtime,
			Root:               root.root,
			ProjectRoot:        root.projectRoot,
			RelativePath:       filepath.ToSlash(filepath.Join(entry.Name(), "SKILL.md")),
			SourceKind:         root.sourceKind,
			MachineFingerprint: fp,
			Editable:           editable,
		})
	}
	return out, nil
}

func discoverLegacyCommands(root rootDef) ([]protocol.SkillInventoryEntry, error) {
	info, err := os.Stat(root.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat legacy command root %q: %w", root.root, err)
	}
	if !info.IsDir() {
		return nil, nil
	}

	dirEntries, err := os.ReadDir(root.root)
	if err != nil {
		return nil, fmt.Errorf("read legacy command root %q: %w", root.root, err)
	}

	out := make([]protocol.SkillInventoryEntry, 0, len(dirEntries))
	for _, entry := range dirEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root.root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read legacy command %q: %w", entry.Name(), err)
		}
		slugName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		out = append(out, protocol.SkillInventoryEntry{
			Slug:               "gsd/" + slugName,
			DisplayName:        slugName,
			Description:        "Legacy GSD command",
			Scope:              root.scope,
			Runtime:            root.runtime,
			Root:               root.root,
			RelativePath:       filepath.ToSlash(entry.Name()),
			SourceKind:         root.sourceKind,
			MachineFingerprint: fingerprint(content),
			Editable:           false,
		})
	}
	return out, nil
}

func parseSkillMetadata(defaultName, content string) (string, string) {
	name := defaultName
	description := ""
	if !strings.HasPrefix(content, "---\n") {
		return name, description
	}
	rest := strings.TrimPrefix(content, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return name, description
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		switch {
		case strings.HasPrefix(line, "name:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			if value != "" {
				name = value
			}
		case strings.HasPrefix(line, "description:"):
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return name, description
}

func fingerprint(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func skillDirFingerprint(skillDir string) (string, error) {
	hasher := sha256.New()
	if err := filepath.WalkDir(skillDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(skillDir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if _, err := hasher.Write([]byte(filepath.ToSlash(rel))); err != nil {
			return err
		}
		if _, err := hasher.Write([]byte{0}); err != nil {
			return err
		}
		if _, err := hasher.Write([]byte(info.Mode().String())); err != nil {
			return err
		}
		if _, err := hasher.Write([]byte{0}); err != nil {
			return err
		}
		if _, err := hasher.Write(data); err != nil {
			return err
		}
		if _, err := hasher.Write([]byte{0}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("fingerprint skill dir %q: %w", skillDir, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
