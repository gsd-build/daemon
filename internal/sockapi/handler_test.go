package sockapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type mockProvider struct {
	health   HealthData
	status   StatusData
	sessions []SessionInfo
}

func (m *mockProvider) Health() HealthData      { return m.health }
func (m *mockProvider) Status() StatusData      { return m.status }
func (m *mockProvider) Sessions() []SessionInfo { return m.sessions }

func TestHealthReturnsOK(t *testing.T) {
	p := &mockProvider{health: HealthData{Status: "ok"}}
	h := newHandler(p)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}
	var got HealthData
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if got.Status != "ok" {
		t.Fatalf("expected status ok, got %s", got.Status)
	}
}

func TestHealthReturns503WhenDisconnected(t *testing.T) {
	p := &mockProvider{health: HealthData{Status: "disconnected"}}
	h := newHandler(p)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	var got HealthData
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if got.Status != "disconnected" {
		t.Fatalf("expected status disconnected, got %s", got.Status)
	}
}

func TestStatusReturnsFullData(t *testing.T) {
	p := &mockProvider{
		status: StatusData{
			Version:            "1.2.3",
			Uptime:             "5m30s",
			RelayConnected:     true,
			RelayURL:           "wss://relay.gsd.build",
			MachineID:          "machine-abc",
			ActiveSessions:     2,
			InFlightTasks:      1,
			MaxConcurrentTasks: 5,
			LogLevel:           "info",
		},
	}
	h := newHandler(p)

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var got StatusData
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if got.Version != "1.2.3" {
		t.Errorf("expected version 1.2.3, got %s", got.Version)
	}
	if got.Uptime != "5m30s" {
		t.Errorf("expected uptime 5m30s, got %s", got.Uptime)
	}
	if !got.RelayConnected {
		t.Error("expected relayConnected true")
	}
	if got.RelayURL != "wss://relay.gsd.build" {
		t.Errorf("expected relayURL wss://relay.gsd.build, got %s", got.RelayURL)
	}
	if got.MachineID != "machine-abc" {
		t.Errorf("expected machineID machine-abc, got %s", got.MachineID)
	}
	if got.ActiveSessions != 2 {
		t.Errorf("expected activeSessions 2, got %d", got.ActiveSessions)
	}
	if got.InFlightTasks != 1 {
		t.Errorf("expected inFlightTasks 1, got %d", got.InFlightTasks)
	}
	if got.MaxConcurrentTasks != 5 {
		t.Errorf("expected maxConcurrentTasks 5, got %d", got.MaxConcurrentTasks)
	}
	if got.LogLevel != "info" {
		t.Errorf("expected logLevel info, got %s", got.LogLevel)
	}
}

func TestSessionsReturnsActiveList(t *testing.T) {
	now := time.Now()
	p := &mockProvider{
		sessions: []SessionInfo{
			{
				SessionID: "sess-1",
				State:     "executing",
				TaskID:    "task-abc",
				StartedAt: &now,
				IdleSince: nil,
			},
			{
				SessionID: "sess-2",
				State:     "idle",
				TaskID:    "",
				StartedAt: nil,
				IdleSince: &now,
			},
		},
	}
	h := newHandler(p)

	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var got []SessionInfo
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got))
	}
	if got[0].SessionID != "sess-1" || got[0].State != "executing" || got[0].TaskID != "task-abc" {
		t.Errorf("unexpected first session: %+v", got[0])
	}
	if got[1].SessionID != "sess-2" || got[1].State != "idle" || got[1].TaskID != "" {
		t.Errorf("unexpected second session: %+v", got[1])
	}
}

func TestSessionsReturnsEmptyArray(t *testing.T) {
	p := &mockProvider{sessions: []SessionInfo{}}
	h := newHandler(p)

	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if body != "[]\n" {
		t.Fatalf("expected body to be []\\n, got %q", body)
	}
}
