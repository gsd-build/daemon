package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/sockapi"
)

func TestFormatWorkersShowsWorkerState(t *testing.T) {
	idleSince := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	out := formatWorkers(&sockapi.StatusData{
		WarmWorkersEnabled: true,
		WarmWorkerIdleTTL:  "20m0s",
		WarmWorkerIdleCap:  4,
		ActiveWarmWorkers:  0,
		IdleWarmWorkers:    1,
	}, []sockapi.WorkerInfo{
		{
			SessionID: "sess-1",
			State:     "idle",
			Provider:  "claude-cli",
			Model:     "claude-sonnet-4-6",
			PID:       4242,
			IdleSince: &idleSince,
		},
	}, idleSince.Add(15*time.Second))

	for _, want := range []string{"warm workers: on", "sess-1", "idle", "claude-cli", "4242", "15s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("workers output missing %q:\n%s", want, out)
		}
	}
}

func TestFormatWorkersShowsEmptyState(t *testing.T) {
	out := formatWorkers(&sockapi.StatusData{WarmWorkersEnabled: false}, nil, time.Now())
	if !strings.Contains(out, "warm workers: off") || !strings.Contains(out, "workers: none") {
		t.Fatalf("unexpected empty workers output:\n%s", out)
	}
}
