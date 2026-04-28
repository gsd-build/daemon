package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, root string, dir string, body string) string {
	t.Helper()
	path := filepath.Join(root, dir, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return path
}

func TestDiscoverClaudeSkillsFindsHomeAndAncestorSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "repo", "pkg")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}

	projectSkill := writeSkill(t, filepath.Join(home, "repo", ".claude", "skills"), "project-skill", `---
name: project-skill
description: Project scoped workflow
---
Use project context.
`)
	homeSkill := writeSkill(t, filepath.Join(home, ".claude", "skills"), "home-skill", `---
name: home-skill
description: Home scoped workflow
---
Use home context.
`)

	got, err := DiscoverClaudeSkills(project)
	if err != nil {
		t.Fatalf("DiscoverClaudeSkills: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 skills, got %+v", got)
	}
	if got[0].Name != "project-skill" || got[0].Description != "Project scoped workflow" || got[0].Path != projectSkill || got[0].Scope != "project" {
		t.Fatalf("unexpected project skill: %+v", got[0])
	}
	if got[1].Name != "home-skill" || got[1].Description != "Home scoped workflow" || got[1].Path != homeSkill || got[1].Scope != "home" {
		t.Fatalf("unexpected home skill: %+v", got[1])
	}
}

func TestDiscoverClaudeSkillsPrefersNearestDuplicateName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "repo", "app")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}

	localPath := writeSkill(t, filepath.Join(project, ".claude", "skills"), "same", `---
name: same
description: Local
---
`)
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "same", `---
name: same
description: Home
---
`)

	got, err := DiscoverClaudeSkills(project)
	if err != nil {
		t.Fatalf("DiscoverClaudeSkills: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected duplicate to collapse, got %+v", got)
	}
	if got[0].Path != localPath || got[0].Description != "Local" {
		t.Fatalf("unexpected duplicate winner: %+v", got[0])
	}
}

func TestDiscoverClaudeSkillsUsesDirectoryNameFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSkill(t, filepath.Join(home, ".claude", "skills"), "fallback", `Use fallback name.`)

	got, err := DiscoverClaudeSkills("")
	if err != nil {
		t.Fatalf("DiscoverClaudeSkills: %v", err)
	}
	if len(got) != 1 || got[0].Name != "fallback" {
		t.Fatalf("unexpected fallback skill: %+v", got)
	}
}
