package crons

import (
	"log/slog"
	"testing"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

type staticStore struct {
	items []LocalCron
}

func (s staticStore) List() ([]LocalCron, error) {
	return s.items, nil
}

func testCronSpec(id, expr string) protocol.CronSpec {
	return protocol.CronSpec{
		ID:             id,
		Name:           "Nightly",
		CronExpression: expr,
		Prompt:         "run tests",
		Mode:           "fresh",
		Model:          "claude-opus-4-6[1m]",
		Effort:         "max",
		ProjectID:      "project-1",
		Enabled:        true,
	}
}

func TestSchedulerEmitsDueOccurrencesOnce(t *testing.T) {
	var due []time.Time
	s := NewScheduler(
		staticStore{items: []LocalCron{{Spec: testCronSpec("cron-1", "* * * * *")}}},
		func(spec protocol.CronSpec, scheduledFor time.Time) error {
			due = append(due, scheduledFor)
			return nil
		},
		time.Minute,
		slog.Default(),
	)

	s.lastCheck = time.Date(2026, 4, 14, 13, 0, 0, 0, time.UTC)
	s.tickAt(time.Date(2026, 4, 14, 13, 2, 0, 0, time.UTC))
	s.tickAt(time.Date(2026, 4, 14, 13, 2, 30, 0, time.UTC))

	if len(due) != 2 {
		t.Fatalf("expected 2 due occurrences, got %d", len(due))
	}
}
