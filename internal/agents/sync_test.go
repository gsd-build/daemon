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

func TestBuildPromptListsAvailableAgents(t *testing.T) {
	prompt := BuildPrompt([]Definition{
		{Name: "explorer", Description: "Maps the code path.", Model: "claude-haiku-4-5-20251001", Tools: []string{"read"}},
		{Name: "reviewer", Description: "Reviews correctness.", Model: "claude-sonnet-4-6", Tools: []string{"read", "search"}},
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
