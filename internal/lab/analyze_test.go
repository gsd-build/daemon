package lab

import "testing"

func TestAnalyzeBundleSummarizesEventsAndErrors(t *testing.T) {
	report, err := AnalyzeBundle(ExportBundle{
		SchemaVersion: 1,
		Config:        SessionConfig{Provider: "claude-cli", Model: "claude-sonnet-4-6"},
		Events: []SessionEvent{
			{Sequence: 1, Kind: "task.started", Payload: map[string]any{"taskId": "t1"}},
			{Sequence: 2, Kind: "tool.call", Payload: map[string]any{"name": "bash"}},
			{Sequence: 3, Kind: "task.error", Payload: map[string]any{"error": "boom"}},
		},
	})
	if err != nil {
		t.Fatalf("AnalyzeBundle: %v", err)
	}
	if report.EventCount != 3 {
		t.Fatalf("EventCount = %d, want 3", report.EventCount)
	}
	if report.ErrorCount != 1 {
		t.Fatalf("ErrorCount = %d, want 1", report.ErrorCount)
	}
	if report.Provider != "claude-cli" {
		t.Fatalf("Provider = %q", report.Provider)
	}
}

func TestAnalyzeBundleRejectsUnsupportedSchema(t *testing.T) {
	_, err := AnalyzeBundle(ExportBundle{SchemaVersion: 99})
	if err == nil {
		t.Fatal("expected unsupported schema error")
	}
}
