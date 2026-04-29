package cmd

import (
	"strings"
	"testing"

	"github.com/gsd-build/daemon/internal/sockapi"
)

func TestFormatLiveStatus(t *testing.T) {
	status := &sockapi.StatusData{
		Version:            "0.1.13",
		Uptime:             "2h34m0s",
		RelayConnected:     true,
		RelayURL:           "wss://relay.gsd.build/ws/daemon",
		MachineID:          "abc-123",
		ActiveSessions:     3,
		InFlightTasks:      1,
		MaxConcurrentTasks: 10,
		WarmWorkersEnabled: true,
		WarmWorkerIdleTTL:  "20m0s",
		WarmWorkerIdleCap:  4,
		ActiveWarmWorkers:  1,
		IdleWarmWorkers:    2,
		LogLevel:           "info",
	}
	sessions := []sockapi.SessionInfo{
		{SessionID: "s1", State: "executing", TaskID: "t1"},
		{SessionID: "s2", State: "idle"},
		{SessionID: "s3", State: "idle"},
	}

	out := formatLiveStatus(status, sessions)

	if !strings.Contains(out, "gsd-cloud v0.1.13") {
		t.Errorf("missing version line in:\n%s", out)
	}
	if !strings.Contains(out, "connected") {
		t.Errorf("missing connected status in:\n%s", out)
	}
	if !strings.Contains(out, "2h34m0s") {
		t.Errorf("missing uptime in:\n%s", out)
	}
	if !strings.Contains(out, "3 active") {
		t.Errorf("missing session count in:\n%s", out)
	}
	if !strings.Contains(out, "1 in-flight") {
		t.Errorf("missing task count in:\n%s", out)
	}
	if !strings.Contains(out, "warm:") || !strings.Contains(out, "1 active, 2 idle") {
		t.Errorf("missing warm worker status in:\n%s", out)
	}
}

func TestFormatStaticStatus(t *testing.T) {
	out := formatStaticStatus("0.1.13", "m-1", "wss://relay.gsd.build/ws/daemon", true)

	if !strings.Contains(out, "gsd-cloud v0.1.13") {
		t.Errorf("missing version in:\n%s", out)
	}
	if !strings.Contains(out, "not running") {
		t.Errorf("missing 'not running' in:\n%s", out)
	}
	if !strings.Contains(out, "gsd-cloud start") {
		t.Errorf("missing start hint in:\n%s", out)
	}
}
