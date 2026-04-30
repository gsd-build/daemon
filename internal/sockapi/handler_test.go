package sockapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mockProvider struct {
	health   HealthData
	status   StatusData
	sessions []SessionInfo
	workers  []WorkerInfo
}

func (m *mockProvider) Health() HealthData      { return m.health }
func (m *mockProvider) Status() StatusData      { return m.status }
func (m *mockProvider) Sessions() []SessionInfo { return m.sessions }
func (m *mockProvider) Workers() []WorkerInfo   { return m.workers }

type mockSubagentProvider struct {
	mockProvider
}

func (m *mockSubagentProvider) CreateSubagentChild(_ *http.Request, _ CreateSubagentChildRequest) (CreateSubagentChildResponse, error) {
	return CreateSubagentChildResponse{}, nil
}

func (m *mockSubagentProvider) ForwardSubagentEvent(_ *http.Request, _ ForwardSubagentEventRequest) error {
	return nil
}

func (m *mockSubagentProvider) RegisterSubagentProcess(_ *http.Request, _ RegisterSubagentProcessRequest) error {
	return nil
}

func (m *mockSubagentProvider) FinalizeSubagentChild(_ *http.Request, _ FinalizeSubagentChildRequest) (FinalizeSubagentChildResponse, error) {
	return FinalizeSubagentChildResponse{}, nil
}

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
			WarmWorkersEnabled: true,
			WarmWorkerIdleTTL:  "20m0s",
			WarmWorkerIdleCap:  4,
			ActiveWarmWorkers:  1,
			IdleWarmWorkers:    1,
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
	if !got.WarmWorkersEnabled {
		t.Error("expected warmWorkersEnabled true")
	}
	if got.WarmWorkerIdleTTL != "20m0s" {
		t.Errorf("expected warmWorkerIdleTTL 20m0s, got %s", got.WarmWorkerIdleTTL)
	}
	if got.WarmWorkerIdleCap != 4 {
		t.Errorf("expected warmWorkerIdleCap 4, got %d", got.WarmWorkerIdleCap)
	}
	if got.ActiveWarmWorkers != 1 {
		t.Errorf("expected activeWarmWorkers 1, got %d", got.ActiveWarmWorkers)
	}
	if got.IdleWarmWorkers != 1 {
		t.Errorf("expected idleWarmWorkers 1, got %d", got.IdleWarmWorkers)
	}
	if got.LogLevel != "info" {
		t.Errorf("expected logLevel info, got %s", got.LogLevel)
	}
}

func TestSubagentMalformedJSONReturnsJSONError(t *testing.T) {
	p := &mockSubagentProvider{}
	h := newHandler(p)

	req := httptest.NewRequest(http.MethodPost, "/subagents/create-child", strings.NewReader("{"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}
	var got map[string]string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(got["error"], "invalid json") {
		t.Fatalf("error = %q, want invalid json", got["error"])
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

func TestWorkersReturnsActiveList(t *testing.T) {
	now := time.Now().UTC()
	idle := now.Add(-30 * time.Second)
	p := &mockProvider{
		workers: []WorkerInfo{
			{
				SessionID:  "sess-1",
				Provider:   "claude-cli",
				Model:      "claude-sonnet-4-6",
				PID:        1234,
				KeyHash:    "abc123",
				State:      "idle",
				StartedAt:  now,
				LastUsedAt: idle,
				IdleSince:  &idle,
			},
		},
	}
	h := newHandler(p)

	req := httptest.NewRequest(http.MethodGet, "/workers", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var got []WorkerInfo
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(got))
	}
	if got[0].SessionID != "sess-1" || got[0].Provider != "claude-cli" || got[0].PID != 1234 {
		t.Fatalf("unexpected worker: %+v", got[0])
	}
	if got[0].State != "idle" || got[0].KeyHash != "abc123" {
		t.Fatalf("unexpected worker state: %+v", got[0])
	}
}
