package crons

import (
	"testing"
	"time"
)

func TestBuildInventoryIncludesNextRunAndLastRun(t *testing.T) {
	lastRun := time.Date(2026, 4, 14, 13, 0, 0, 0, time.UTC)
	syncedAt := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 14, 13, 30, 0, 0, time.UTC)

	items := BuildInventory([]LocalCron{{
		Spec:            testCronSpec("cron-1", "* * * * *"),
		SyncedAt:        syncedAt,
		LastRunAt:       &lastRun,
		LocallyModified: true,
	}}, now)

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].LastRunAt == "" || items[0].NextRunAt == "" {
		t.Fatalf("expected lastRunAt and nextRunAt, got %+v", items[0])
	}
	if !items[0].LocallyModified {
		t.Fatal("expected locallyModified to be preserved")
	}
}
