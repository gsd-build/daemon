package crons

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

func TestStoreSyncListAndRecordRun(t *testing.T) {
	store := NewStore(t.TempDir())
	syncedAt := time.Date(2026, 4, 14, 13, 0, 0, 0, time.UTC)

	if err := store.Sync([]protocol.CronSpec{{
		ID:             "cron-1",
		Name:           "Nightly",
		CronExpression: "0 3 * * *",
		Prompt:         "run tests",
		Mode:           "fresh",
		Model:          "claude-opus-4-6[1m]",
		Effort:         "max",
		ProjectID:      "project-1",
		Enabled:        true,
	}}, syncedAt); err != nil {
		t.Fatalf("sync: %v", err)
	}

	locals, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(locals) != 1 {
		t.Fatalf("expected 1 local cron, got %d", len(locals))
	}
	if locals[0].LocallyModified {
		t.Fatal("expected freshly synced cron to not be locally modified")
	}
	if !locals[0].SyncedAt.Equal(syncedAt) {
		t.Fatalf("unexpected syncedAt: %s", locals[0].SyncedAt)
	}

	runAt := syncedAt.Add(2 * time.Hour)
	if err := store.RecordRun("cron-1", runAt); err != nil {
		t.Fatalf("record run: %v", err)
	}

	locals, err = store.List()
	if err != nil {
		t.Fatalf("list after record run: %v", err)
	}
	if locals[0].LastRunAt == nil || !locals[0].LastRunAt.Equal(runAt) {
		t.Fatalf("unexpected lastRunAt: %+v", locals[0].LastRunAt)
	}
}

func TestStoreDetectsLocalModification(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Sync([]protocol.CronSpec{{
		ID:             "cron-1",
		Name:           "Nightly",
		CronExpression: "0 3 * * *",
		Prompt:         "run tests",
		Mode:           "fresh",
		Model:          "claude-opus-4-6[1m]",
		Effort:         "max",
		ProjectID:      "project-1",
		Enabled:        true,
	}}, time.Now().UTC()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if err := os.WriteFile(filepath.Join(store.Dir(), "cron-1.json"), []byte(`{"id":"cron-1","name":"Edited","cronExpression":"* * * * *","prompt":"run tests","mode":"fresh","model":"claude-opus-4-6[1m]","effort":"max","projectId":"project-1","enabled":true}`), 0o600); err != nil {
		t.Fatalf("write local edit: %v", err)
	}

	locals, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(locals) != 1 || !locals[0].LocallyModified {
		t.Fatalf("expected local modification to be detected, got %+v", locals)
	}
}
