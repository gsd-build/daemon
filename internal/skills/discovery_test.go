package skills

import (
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/gsd-build/protocol-go"
)

func TestDiscoverClassifiesCanonicalRoots(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	writeSkillFixture(t, filepath.Join(project, ".claude", "skills"), "project-alpha", "Project skill")
	writeSkillFixture(t, filepath.Join(project, ".codex", "skills"), "project-codex", "Project Codex skill")
	writeSkillFixture(t, filepath.Join(home, ".claude", "skills"), "global-claude", "Global Claude skill")
	writeSkillFixture(t, filepath.Join(home, ".codex", "skills"), "global-codex", "Global Codex skill")
	writeSkillFixture(t, filepath.Join(home, ".claude", "skills"), "gsd-help", "Installed skill")
	writeSkillFixture(t, filepath.Join(home, ".claude", "get-shit-done", "skills"), "legacy-import", "Deprecated import-only skill")

	legacyCommandsDir := filepath.Join(home, ".claude", "commands", "gsd")
	if err := os.MkdirAll(legacyCommandsDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy commands: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyCommandsDir, "help.md"), []byte("# help"), 0o644); err != nil {
		t.Fatalf("write legacy command: %v", err)
	}

	entries, err := Discover(DiscoverOptions{
		HomeDir:      home,
		ProjectRoots: []string{project},
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	bySlug := map[string]protocol.SkillInventoryEntry{}
	for _, entry := range entries {
		bySlug[entry.Slug] = entry
	}

	assertInventoryEntry(t, bySlug, "project-alpha", protocol.SkillInventoryEntry{
		Slug:         "project-alpha",
		Scope:        "project",
		Runtime:      "claude",
		ProjectRoot:  project,
		RelativePath: "project-alpha/SKILL.md",
		SourceKind:   "skill_dir",
		Editable:     false,
	})
	assertInventoryEntry(t, bySlug, "project-codex", protocol.SkillInventoryEntry{
		Slug:         "project-codex",
		Scope:        "project",
		Runtime:      "codex",
		ProjectRoot:  project,
		RelativePath: "project-codex/SKILL.md",
		SourceKind:   "skill_dir",
		Editable:     false,
	})
	assertInventoryEntry(t, bySlug, "global-claude", protocol.SkillInventoryEntry{
		Slug:         "global-claude",
		Scope:        "global",
		Runtime:      "claude",
		RelativePath: "global-claude/SKILL.md",
		SourceKind:   "skill_dir",
		Editable:     true,
	})
	assertInventoryEntry(t, bySlug, "global-codex", protocol.SkillInventoryEntry{
		Slug:         "global-codex",
		Scope:        "global",
		Runtime:      "codex",
		RelativePath: "global-codex/SKILL.md",
		SourceKind:   "skill_dir",
		Editable:     true,
	})
	assertInventoryEntry(t, bySlug, "gsd-help", protocol.SkillInventoryEntry{
		Slug:         "gsd-help",
		Scope:        "installed",
		Runtime:      "claude",
		RelativePath: "gsd-help/SKILL.md",
		SourceKind:   "skill_dir",
		Editable:     false,
	})
	assertInventoryEntry(t, bySlug, "legacy-import", protocol.SkillInventoryEntry{
		Slug:         "legacy-import",
		Scope:        "global",
		Runtime:      "claude",
		RelativePath: "legacy-import/SKILL.md",
		SourceKind:   "skill_dir",
		Editable:     false,
	})
	assertInventoryEntry(t, bySlug, "gsd/help", protocol.SkillInventoryEntry{
		Slug:         "gsd/help",
		Scope:        "installed",
		Runtime:      "claude",
		RelativePath: "help.md",
		SourceKind:   "legacy_command",
		Editable:     false,
	})
}

func TestDiscoverSkipsDirectoriesWithoutSkillMarkdown(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	if err := os.MkdirAll(filepath.Join(project, ".claude", "skills", "notes"), 0o755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	writeSkillFixture(t, filepath.Join(home, ".claude", "skills"), "valid-skill", "Valid skill")

	entries, err := Discover(DiscoverOptions{
		HomeDir:      home,
		ProjectRoots: []string{project},
	})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Slug != "valid-skill" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

func TestDiscoverFingerprintChangesWithSkillContent(t *testing.T) {
	home := t.TempDir()
	managedRoot := filepath.Join(home, ".claude", "skills")
	writeSkillFixture(t, managedRoot, "sync-skill", "First")

	entries, err := Discover(DiscoverOptions{HomeDir: home})
	if err != nil {
		t.Fatalf("discover initial: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	first := entries[0].MachineFingerprint

	writeSkillFixture(t, managedRoot, "sync-skill", "Second")

	entries, err = Discover(DiscoverOptions{HomeDir: home})
	if err != nil {
		t.Fatalf("discover updated: %v", err)
	}
	second := entries[0].MachineFingerprint
	if first == second {
		t.Fatal("expected fingerprint to change when SKILL.md changes")
	}
}

func writeSkillFixture(t *testing.T, root, name, description string) {
	t.Helper()

	skillDir := filepath.Join(root, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func assertInventoryEntry(t *testing.T, bySlug map[string]protocol.SkillInventoryEntry, slug string, want protocol.SkillInventoryEntry) {
	t.Helper()

	got, ok := bySlug[slug]
	if !ok {
		t.Fatalf("missing slug %q in inventory: %+v", slug, bySlug)
	}
	if got.DisplayName == "" {
		t.Fatalf("expected display name for %q", slug)
	}
	if got.Description == "" {
		t.Fatalf("expected description for %q", slug)
	}
	if got.MachineFingerprint == "" {
		t.Fatalf("expected fingerprint for %q", slug)
	}
	if got.Scope != want.Scope || got.Runtime != want.Runtime || got.ProjectRoot != want.ProjectRoot || got.RelativePath != want.RelativePath || got.SourceKind != want.SourceKind || got.Editable != want.Editable {
		t.Fatalf("unexpected entry for %q: got %+v want %+v", slug, got, want)
	}
}
