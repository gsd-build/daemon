package lab

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionStoreAppendsJSONLAndExportsOrderedBundle(t *testing.T) {
	store, err := NewSessionStore(SessionStoreOptions{
		RootDir: t.TempDir(),
		Config: SessionConfig{
			CWD:            "/tmp/project",
			Provider:       "claude-cli",
			Model:          "claude-sonnet-4-6",
			Effort:         "medium",
			PermissionMode: "acceptEdits",
			FakeMode:       false,
		},
	})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	if err := store.Append("relay.daemon.recv", map[string]any{"type": "hello"}); err != nil {
		t.Fatalf("append hello: %v", err)
	}
	if err := store.Append("relay.daemon.send", map[string]any{"type": "welcome"}); err != nil {
		t.Fatalf("append welcome: %v", err)
	}

	bundle, err := store.Export(ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if bundle.SchemaVersion != 1 {
		t.Fatalf("schema version = %d, want 1", bundle.SchemaVersion)
	}
	if len(bundle.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(bundle.Events))
	}
	if bundle.Events[0].Sequence != 1 || bundle.Events[1].Sequence != 2 {
		t.Fatalf("sequences = %d,%d", bundle.Events[0].Sequence, bundle.Events[1].Sequence)
	}

	jsonlPath := filepath.Join(store.Dir(), "session.jsonl")
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl lines = %d, want 2", len(lines))
	}
	var first SessionEvent
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("jsonl first line parses: %v", err)
	}
}

func TestSessionStoreRedactsStructuredPayloadSecrets(t *testing.T) {
	store, err := NewSessionStore(SessionStoreOptions{RootDir: t.TempDir(), Config: SessionConfig{}})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	if err := store.Append("http.request", map[string]any{
		"headers": map[string]any{"authorization": "Bearer sk-secret-value"},
	}); err != nil {
		t.Fatalf("append secret payload: %v", err)
	}
	bundle, err := store.Export(ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	data, err := json.Marshal(bundle.Events[0])
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if strings.Contains(string(data), "sk-secret-value") {
		t.Fatalf("export includes secret: %s", string(data))
	}
	if len(bundle.Events[0].Redacted) != 1 {
		t.Fatalf("redacted records = %d, want 1", len(bundle.Events[0].Redacted))
	}
}
