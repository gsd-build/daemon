package sockapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tempSockPath creates a short temp dir (to stay under macOS's 104-char
// Unix socket path limit) and returns the socket path inside it.
func tempSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gsd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

func waitForSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s did not appear within 2s", sockPath)
}

func TestQueryStatusFromSocket(t *testing.T) {
	sockPath := tempSockPath(t)

	p := &mockProvider{
		status: StatusData{
			Version:            "1.2.3",
			Uptime:             "10m",
			RelayConnected:     true,
			RelayURL:           "wss://relay.gsd.build",
			MachineID:          "machine-xyz",
			ActiveSessions:     3,
			InFlightTasks:      2,
			MaxConcurrentTasks: 5,
			LogLevel:           "info",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(sockPath, p)
	go srv.ListenAndServe(ctx) //nolint:errcheck

	waitForSocket(t, sockPath)

	got, err := QueryStatus(sockPath)
	if err != nil {
		t.Fatalf("QueryStatus: %v", err)
	}
	if got.Version != "1.2.3" {
		t.Errorf("expected version 1.2.3, got %s", got.Version)
	}
	if got.Uptime != "10m" {
		t.Errorf("expected uptime 10m, got %s", got.Uptime)
	}
	if !got.RelayConnected {
		t.Error("expected relayConnected true")
	}
	if got.MachineID != "machine-xyz" {
		t.Errorf("expected machineID machine-xyz, got %s", got.MachineID)
	}
	if got.ActiveSessions != 3 {
		t.Errorf("expected activeSessions 3, got %d", got.ActiveSessions)
	}
}

func TestQueryWorkersFromSocket(t *testing.T) {
	sockPath := tempSockPath(t)
	now := time.Now().UTC()
	idle := now.Add(-5 * time.Second)

	p := &mockProvider{
		workers: []WorkerInfo{
			{
				SessionID:  "sess-1",
				Provider:   "claude-cli",
				Model:      "claude-sonnet-4-6",
				PID:        4242,
				KeyHash:    "hash-a",
				State:      "idle",
				StartedAt:  now,
				LastUsedAt: idle,
				IdleSince:  &idle,
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(sockPath, p)
	go srv.ListenAndServe(ctx) //nolint:errcheck

	waitForSocket(t, sockPath)

	got, err := QueryWorkers(sockPath)
	if err != nil {
		t.Fatalf("QueryWorkers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("workers len = %d, want 1", len(got))
	}
	if got[0].SessionID != "sess-1" || got[0].PID != 4242 || got[0].State != "idle" {
		t.Fatalf("unexpected worker: %+v", got[0])
	}
}

func TestQueryHealthConnected(t *testing.T) {
	sockPath := tempSockPath(t)

	p := &mockProvider{health: HealthData{Status: "ok"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(sockPath, p)
	go srv.ListenAndServe(ctx) //nolint:errcheck

	waitForSocket(t, sockPath)

	got, err := QueryHealth(sockPath)
	if err != nil {
		t.Fatalf("QueryHealth: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("expected status ok, got %s", got.Status)
	}
}

func TestIsDaemonRunningReturnsFalseWhenNoSocket(t *testing.T) {
	sockPath := tempSockPath(t)
	// sockPath does not exist yet — IsDaemonRunning should return false.
	if IsDaemonRunning(sockPath) {
		t.Error("expected false for non-existent socket, got true")
	}
}

func TestIsDaemonRunningReturnsTrueWithServer(t *testing.T) {
	sockPath := tempSockPath(t)

	p := &mockProvider{health: HealthData{Status: "ok"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(sockPath, p)
	go srv.ListenAndServe(ctx) //nolint:errcheck

	waitForSocket(t, sockPath)

	if !IsDaemonRunning(sockPath) {
		t.Error("expected true for running server, got false")
	}
}
