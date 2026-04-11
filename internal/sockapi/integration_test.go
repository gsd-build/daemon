package sockapi

import (
	"context"
	"testing"
	"time"
)

func TestIntegrationFullRoundTrip(t *testing.T) {
	sockPath := tempSockPath(t)

	now := time.Now().UTC()
	idle := now.Add(-5 * time.Minute)

	provider := &mockProvider{
		health: HealthData{Status: "ok"},
		status: StatusData{
			Version:            "0.2.0",
			Uptime:             "10m0s",
			RelayConnected:     true,
			RelayURL:           "wss://relay.gsd.build/ws/daemon",
			MachineID:          "integration-test",
			ActiveSessions:     2,
			InFlightTasks:      1,
			MaxConcurrentTasks: 8,
			LogLevel:           "debug",
		},
		sessions: []SessionInfo{
			{SessionID: "s1", State: "executing", TaskID: "t1", StartedAt: &now},
			{SessionID: "s2", State: "idle", IdleSince: &idle},
		},
	}

	srv := NewServer(sockPath, provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()

	waitForSocket(t, sockPath)

	// 1. Health
	health, err := QueryHealth(sockPath)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health.Status != "ok" {
		t.Errorf("health.Status = %s", health.Status)
	}

	// 2. Status
	status, err := QueryStatus(sockPath)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Version != "0.2.0" {
		t.Errorf("status.Version = %s", status.Version)
	}
	if status.MachineID != "integration-test" {
		t.Errorf("status.MachineID = %s", status.MachineID)
	}
	if status.ActiveSessions != 2 {
		t.Errorf("status.ActiveSessions = %d", status.ActiveSessions)
	}
	if status.InFlightTasks != 1 {
		t.Errorf("status.InFlightTasks = %d", status.InFlightTasks)
	}

	// 3. Sessions
	sessions, err := QuerySessions(sockPath)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].State != "executing" || sessions[0].TaskID != "t1" {
		t.Errorf("session 0: %+v", sessions[0])
	}
	if sessions[1].State != "idle" {
		t.Errorf("session 1: %+v", sessions[1])
	}

	// 4. IsDaemonRunning
	if !IsDaemonRunning(sockPath) {
		t.Error("IsDaemonRunning should return true")
	}

	// 5. Clean shutdown
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("server error: %v", err)
	}

	if IsDaemonRunning(sockPath) {
		t.Error("IsDaemonRunning should return false after shutdown")
	}
}
