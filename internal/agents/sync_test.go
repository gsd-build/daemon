package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncDefinitionsWritesAgentFiles(t *testing.T) {
	dir := t.TempDir()
	defs := []Definition{
		{
			Name:         "explorer",
			Description:  "Maps the code path.",
			SystemPrompt: "Read the code and report findings.",
			Model:        "claude-haiku-4-5-20251001",
			Tools:        []string{"read", "search"},
		},
	}

	if err := SyncDefinitions(dir, defs); err != nil {
		t.Fatalf("SyncDefinitions: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "explorer.json"))
	if err != nil {
		t.Fatalf("read synced agent: %v", err)
	}
	var got Definition
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode synced agent: %v", err)
	}
	if got.Name != "explorer" || got.Model != "claude-haiku-4-5-20251001" {
		t.Fatalf("synced definition = %+v", got)
	}
}

func TestSyncDefinitionsReplacesStaleAgentFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stale.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatalf("write stale agent: %v", err)
	}

	if err := SyncDefinitions(dir, []Definition{{Name: "reviewer", Model: "claude-sonnet-4-6"}}); err != nil {
		t.Fatalf("SyncDefinitions: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("stale agent file stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "reviewer.json")); err != nil {
		t.Fatalf("reviewer agent file stat: %v", err)
	}
}

func TestSyncDefinitionsRejectsFileNameCollisions(t *testing.T) {
	dir := t.TempDir()
	err := SyncDefinitions(dir, []Definition{
		{Name: "a/b"},
		{Name: "a?b"},
	})
	if err == nil {
		t.Fatal("SyncDefinitions returned nil, want collision error")
	}
	if !contains(err.Error(), "resolve to the same file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildPromptListsAvailableAgents(t *testing.T) {
	prompt := BuildPrompt([]Definition{
		{Name: "reviewer", Description: "Reviews correctness.", Model: "claude-sonnet-4-6", Tools: []string{"read", "search"}},
		{Name: "explorer", Description: "Maps the code path.", Model: "claude-haiku-4-5-20251001", Tools: []string{"read"}},
	})

	for _, want := range []string{
		"<subagents>",
		"Use the subagent tool",
		"explorer",
		"reviewer",
		"claude-sonnet-4-6",
	} {
		if !contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if index(prompt, "explorer") > index(prompt, "reviewer") {
		t.Fatalf("prompt order is not deterministic:\n%s", prompt)
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && index(s, substr) >= 0)
}

func index(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
