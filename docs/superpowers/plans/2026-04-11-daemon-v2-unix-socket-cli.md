# Daemon v2: Unix Socket & CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Unix domain socket server exposing health/status/sessions endpoints over HTTP/1.1, and upgrade the `gsd-cloud status` CLI command to query the live daemon via the socket with fallback to static config.

**Architecture:** A new `internal/sockapi` package contains the HTTP handler and a `StatusProvider` interface that the daemon loop implements. The socket server starts as a goroutine from the daemon loop, listens on `~/.gsd-cloud/daemon.sock` with `0600` permissions, and shuts down cleanly with the daemon. The `cmd/status.go` command dials the socket, and falls back to config-only output when the daemon is not running.

**Tech Stack:** Go 1.25, `net/http` over `net.Listen("unix", ...)`, no external deps

**Spec reference:** `docs/superpowers/specs/2026-04-11-daemon-v2-design.md` Sections 9, 10

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/sockapi/provider.go` | `StatusProvider` interface, `SessionInfo` and `StatusData` types |
| `internal/sockapi/server.go` | `Server` struct: start listener, register routes, clean shutdown, stale socket removal |
| `internal/sockapi/handler.go` | HTTP handlers: `handleHealth`, `handleStatus`, `handleSessions` |
| `internal/sockapi/server_test.go` | Tests for socket lifecycle: start, stale cleanup, permissions, shutdown removal |
| `internal/sockapi/handler_test.go` | Tests for all three endpoints with mock provider |
| `internal/sockapi/client.go` | `QueryStatus`, `QueryHealth` — helpers for CLI to dial the socket |
| `internal/sockapi/client_test.go` | Tests for client against a real socket server |
| Modify: `internal/loop/daemon.go` | Implement `StatusProvider`, start/stop socket server in `Run` |
| Modify: `internal/session/manager.go` | Add `SessionInfos()` method returning `[]SessionInfo` |
| Modify: `internal/session/actor.go` | Add `Info()` method returning session state snapshot |
| Modify: `cmd/status.go` | Query socket first, fall back to config-only output |

---

### Task 1: Define StatusProvider interface and types

**Files:**
- Create: `internal/sockapi/provider.go`

- [ ] **Step 1: Write the types and interface**

```go
// internal/sockapi/provider.go
package sockapi

import "time"

// SessionInfo is a snapshot of one active session.
type SessionInfo struct {
	SessionID string     `json:"sessionID"`
	State     string     `json:"state"`     // "executing" or "idle"
	TaskID    string     `json:"taskID"`    // empty when idle
	StartedAt *time.Time `json:"startedAt"` // when current task started; nil when idle
	IdleSince *time.Time `json:"idleSince"` // when actor became idle; nil when executing
}

// StatusData is the full daemon status snapshot.
type StatusData struct {
	Version            string `json:"version"`
	Uptime             string `json:"uptime"`
	RelayConnected     bool   `json:"relayConnected"`
	RelayURL           string `json:"relayURL"`
	MachineID          string `json:"machineID"`
	ActiveSessions     int    `json:"activeSessions"`
	InFlightTasks      int    `json:"inFlightTasks"`
	MaxConcurrentTasks int    `json:"maxConcurrentTasks"`
	LogLevel           string `json:"logLevel"`
}

// HealthData is the health check response.
type HealthData struct {
	Status string `json:"status"` // "ok" or "disconnected"
}

// StatusProvider is implemented by the daemon loop to expose live state.
type StatusProvider interface {
	Health() HealthData
	Status() StatusData
	Sessions() []SessionInfo
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./internal/sockapi/
```

Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/sockapi/provider.go
git commit -m "feat(sockapi): define StatusProvider interface and response types

Types for health, status, and session info snapshots. The daemon loop
will implement StatusProvider; the socket server queries it."
```

---

### Task 2: Implement HTTP handlers with mock provider

**Files:**
- Create: `internal/sockapi/handler.go`
- Create: `internal/sockapi/handler_test.go`

- [ ] **Step 1: Write the failing test for GET /health returning "ok"**

```go
// internal/sockapi/handler_test.go
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

func (m *mockProvider) Health() HealthData       { return m.health }
func (m *mockProvider) Status() StatusData       { return m.status }
func (m *mockProvider) Sessions() []SessionInfo  { return m.sessions }

func TestHealthReturnsOK(t *testing.T) {
	h := newHandler(&mockProvider{
		health: HealthData{Status: "ok"},
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got HealthData
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("expected status=ok, got %s", got.Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestHealthReturnsOK -v
```

Expected: FAIL — `newHandler` not defined.

- [ ] **Step 3: Write the handler implementation**

```go
// internal/sockapi/handler.go
package sockapi

import (
	"encoding/json"
	"net/http"
)

// newHandler returns an http.Handler with all socket API routes registered.
func newHandler(p StatusProvider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		h := p.Health()
		w.Header().Set("Content-Type", "application/json")
		if h.Status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(h)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p.Status())
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p.Sessions())
	})
	return mux
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestHealthReturnsOK -v
```

Expected: PASS

- [ ] **Step 5: Write test for GET /health returning 503 when disconnected**

```go
// append to internal/sockapi/handler_test.go

func TestHealthReturns503WhenDisconnected(t *testing.T) {
	h := newHandler(&mockProvider{
		health: HealthData{Status: "disconnected"},
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	var got HealthData
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != "disconnected" {
		t.Errorf("expected status=disconnected, got %s", got.Status)
	}
}
```

- [ ] **Step 6: Run test**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestHealthReturns503 -v
```

Expected: PASS

- [ ] **Step 7: Write test for GET /status**

```go
// append to internal/sockapi/handler_test.go

func TestStatusReturnsFullData(t *testing.T) {
	h := newHandler(&mockProvider{
		status: StatusData{
			Version:            "0.1.13",
			Uptime:             "2h34m",
			RelayConnected:     true,
			RelayURL:           "wss://relay.gsd.build/ws/daemon",
			MachineID:          "abc-123",
			ActiveSessions:     3,
			InFlightTasks:      1,
			MaxConcurrentTasks: 10,
			LogLevel:           "info",
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got StatusData
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != "0.1.13" {
		t.Errorf("version: got %s", got.Version)
	}
	if got.ActiveSessions != 3 {
		t.Errorf("activeSessions: got %d", got.ActiveSessions)
	}
	if got.InFlightTasks != 1 {
		t.Errorf("inFlightTasks: got %d", got.InFlightTasks)
	}
	if !got.RelayConnected {
		t.Error("expected relayConnected=true")
	}
}
```

- [ ] **Step 8: Run test**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestStatusReturnsFullData -v
```

Expected: PASS

- [ ] **Step 9: Write test for GET /sessions**

```go
// append to internal/sockapi/handler_test.go

func TestSessionsReturnsActiveList(t *testing.T) {
	now := time.Now().UTC()
	idle := now.Add(-15 * time.Minute)
	h := newHandler(&mockProvider{
		sessions: []SessionInfo{
			{
				SessionID: "sess-1",
				State:     "executing",
				TaskID:    "task-42",
				StartedAt: &now,
			},
			{
				SessionID: "sess-2",
				State:     "idle",
				IdleSince: &idle,
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []SessionInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got))
	}
	if got[0].SessionID != "sess-1" || got[0].State != "executing" {
		t.Errorf("session 0: %+v", got[0])
	}
	if got[1].SessionID != "sess-2" || got[1].State != "idle" {
		t.Errorf("session 1: %+v", got[1])
	}
}
```

- [ ] **Step 10: Write test for GET /sessions returning empty array (not null)**

```go
// append to internal/sockapi/handler_test.go

func TestSessionsReturnsEmptyArray(t *testing.T) {
	h := newHandler(&mockProvider{
		sessions: []SessionInfo{},
	})

	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if body != "[]\n" {
		t.Errorf("expected empty JSON array, got: %q", body)
	}
}
```

- [ ] **Step 11: Run all handler tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run 'Test(Health|Status|Sessions)' -v
```

Expected: all PASS

- [ ] **Step 12: Commit**

```bash
git add internal/sockapi/handler.go internal/sockapi/handler_test.go
git commit -m "feat(sockapi): HTTP handlers for /health, /status, /sessions

Three endpoints served over the Unix socket. /health returns 200 or 503
based on relay connection state. /status returns full daemon snapshot.
/sessions returns active session list as JSON array."
```

---

### Task 3: Implement socket server lifecycle

**Files:**
- Create: `internal/sockapi/server.go`
- Create: `internal/sockapi/server_test.go`

- [ ] **Step 1: Write the failing test for server start and shutdown**

```go
// internal/sockapi/server_test.go
package sockapi

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerStartAndShutdown(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

	provider := &mockProvider{
		health: HealthData{Status: "ok"},
	}
	srv := NewServer(sockPath, provider)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx)
	}()

	// Wait for socket to appear
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify socket exists
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("socket not created: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %04o", info.Mode().Perm())
	}

	// Verify we can connect
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()

	// Shutdown
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("ListenAndServe returned error: %v", err)
	}

	// Socket file removed after shutdown
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("socket file should be removed after shutdown")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestServerStartAndShutdown -v
```

Expected: FAIL — `NewServer` not defined.

- [ ] **Step 3: Write the server implementation**

```go
// internal/sockapi/server.go
package sockapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

// Server serves the status API over a Unix domain socket.
type Server struct {
	sockPath string
	httpSrv  *http.Server
}

// NewServer creates a Server that will listen on sockPath.
func NewServer(sockPath string, provider StatusProvider) *Server {
	return &Server{
		sockPath: sockPath,
		httpSrv: &http.Server{
			Handler: newHandler(provider),
		},
	}
}

// ListenAndServe starts the Unix socket listener and serves HTTP until
// ctx is cancelled. Removes stale socket files on startup and cleans up
// on shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(s.sockPath), 0700); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}

	// Remove stale socket from a previous crash.
	os.Remove(s.sockPath)

	ln, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}

	// Set owner-only permissions.
	if err := os.Chmod(s.sockPath, 0600); err != nil {
		ln.Close()
		os.Remove(s.sockPath)
		return fmt.Errorf("chmod socket: %w", err)
	}

	// Shut down when context is cancelled.
	go func() {
		<-ctx.Done()
		s.httpSrv.Shutdown(context.Background())
	}()

	err = s.httpSrv.Serve(ln)
	// Clean up socket file on exit.
	os.Remove(s.sockPath)

	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestServerStartAndShutdown -v
```

Expected: PASS

- [ ] **Step 5: Write test for stale socket cleanup on startup**

```go
// append to internal/sockapi/server_test.go

func TestServerCleansStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

	// Create a stale socket file (simulating a crash).
	if err := os.WriteFile(sockPath, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}

	provider := &mockProvider{
		health: HealthData{Status: "ok"},
	}
	srv := NewServer(sockPath, provider)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx)
	}()

	// Wait for socket to appear
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify it's a real socket, not the stale file.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("should be able to connect after stale cleanup: %v", err)
	}
	conn.Close()

	cancel()
	<-errCh
}
```

- [ ] **Step 6: Run test**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestServerCleansStaleSocket -v
```

Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/sockapi/server.go internal/sockapi/server_test.go
git commit -m "feat(sockapi): Unix domain socket server with lifecycle management

Listens on ~/.gsd-cloud/daemon.sock with 0600 permissions. Removes
stale sockets on startup and cleans up on shutdown. Serves HTTP/1.1
over the socket using the handler from handler.go."
```

---

### Task 4: Implement socket client helpers for CLI

**Files:**
- Create: `internal/sockapi/client.go`
- Create: `internal/sockapi/client_test.go`

- [ ] **Step 1: Write the failing test for QueryStatus against a live server**

```go
// internal/sockapi/client_test.go
package sockapi

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryStatusFromSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

	provider := &mockProvider{
		status: StatusData{
			Version:            "0.1.13",
			Uptime:             "1h",
			RelayConnected:     true,
			RelayURL:           "wss://relay.gsd.build/ws/daemon",
			MachineID:          "m-1",
			ActiveSessions:     2,
			InFlightTasks:      1,
			MaxConcurrentTasks: 10,
			LogLevel:           "info",
		},
		sessions: []SessionInfo{},
	}
	srv := NewServer(sockPath, provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.ListenAndServe(ctx)

	// Wait for socket
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, err := QueryStatus(sockPath)
	if err != nil {
		t.Fatalf("QueryStatus: %v", err)
	}
	if got.Version != "0.1.13" {
		t.Errorf("version: got %s", got.Version)
	}
	if got.ActiveSessions != 2 {
		t.Errorf("activeSessions: got %d", got.ActiveSessions)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestQueryStatusFromSocket -v
```

Expected: FAIL — `QueryStatus` not defined.

- [ ] **Step 3: Write the client implementation**

```go
// internal/sockapi/client.go
package sockapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// unixHTTPClient returns an http.Client that dials the given Unix socket.
func unixHTTPClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}

// QueryHealth checks daemon health via the Unix socket.
// Returns an error if the socket doesn't exist or the daemon is unreachable.
func QueryHealth(sockPath string) (*HealthData, error) {
	client := unixHTTPClient(sockPath)
	resp, err := client.Get("http://daemon/health")
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	var data HealthData
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode health: %w", err)
	}
	return &data, nil
}

// QueryStatus fetches the full daemon status via the Unix socket.
func QueryStatus(sockPath string) (*StatusData, error) {
	client := unixHTTPClient(sockPath)
	resp, err := client.Get("http://daemon/status")
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	var data StatusData
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	return &data, nil
}

// QuerySessions fetches the active session list via the Unix socket.
func QuerySessions(sockPath string) ([]SessionInfo, error) {
	client := unixHTTPClient(sockPath)
	resp, err := client.Get("http://daemon/sessions")
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	var data []SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	return data, nil
}

// IsDaemonRunning checks whether the daemon is reachable on the socket.
func IsDaemonRunning(sockPath string) bool {
	conn, err := net.DialTimeout("unix", sockPath, 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestQueryStatusFromSocket -v
```

Expected: PASS

- [ ] **Step 5: Write test for QueryHealth**

```go
// append to internal/sockapi/client_test.go

func TestQueryHealthConnected(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

	provider := &mockProvider{
		health:   HealthData{Status: "ok"},
		sessions: []SessionInfo{},
	}
	srv := NewServer(sockPath, provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.ListenAndServe(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, err := QueryHealth(sockPath)
	if err != nil {
		t.Fatalf("QueryHealth: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("expected ok, got %s", got.Status)
	}
}
```

- [ ] **Step 6: Write test for IsDaemonRunning returning false when no socket**

```go
// append to internal/sockapi/client_test.go

func TestIsDaemonRunningReturnsFalseWhenNoSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "does-not-exist.sock")
	if IsDaemonRunning(sockPath) {
		t.Error("expected false when socket does not exist")
	}
}
```

- [ ] **Step 7: Write test for IsDaemonRunning returning true with live server**

```go
// append to internal/sockapi/client_test.go

func TestIsDaemonRunningReturnsTrueWithServer(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

	srv := NewServer(sockPath, &mockProvider{
		health:   HealthData{Status: "ok"},
		sessions: []SessionInfo{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.ListenAndServe(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !IsDaemonRunning(sockPath) {
		t.Error("expected true when server is running")
	}
}
```

- [ ] **Step 8: Run all client tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run 'Test(Query|IsDaemon)' -v
```

Expected: all PASS

- [ ] **Step 9: Commit**

```bash
git add internal/sockapi/client.go internal/sockapi/client_test.go
git commit -m "feat(sockapi): client helpers for CLI to query daemon via Unix socket

QueryHealth, QueryStatus, QuerySessions dial the socket and decode
JSON responses. IsDaemonRunning does a quick dial check."
```

---

### Task 5: Add session info methods to actor and manager

**Files:**
- Modify: `internal/session/actor.go`
- Modify: `internal/session/manager.go`

- [ ] **Step 1: Write test for Actor.Info() returning executing state**

```go
// append to internal/session/actor_test.go (or create a new section)

func TestActorInfoExecutingState(t *testing.T) {
	actor, err := NewActor(Options{
		SessionID:  "sess-1",
		BinaryPath: "echo",
		CWD:        "/tmp",
		Relay:      &mockRelay{},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate an in-flight task by setting taskID and taskStartedAt.
	actor.taskMu.Lock()
	actor.taskID = "task-42"
	now := time.Now()
	actor.taskStartedAt = &now
	actor.taskMu.Unlock()

	info := actor.Info()
	if info.State != "executing" {
		t.Errorf("expected executing, got %s", info.State)
	}
	if info.TaskID != "task-42" {
		t.Errorf("expected task-42, got %s", info.TaskID)
	}
	if info.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
	if info.IdleSince != nil {
		t.Error("expected IdleSince to be nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run TestActorInfoExecutingState -v
```

Expected: FAIL — `Info` method and `taskStartedAt` field not defined.

- [ ] **Step 3: Add taskStartedAt and idleSince fields to Actor, add Info() method**

Add to `internal/session/actor.go`:

In the `Actor` struct, add these fields alongside the existing `taskMu`/`taskCancel`/`taskID` fields:

```go
	taskStartedAt *time.Time       // when current task started; nil when idle
	idleSince     *time.Time       // when actor became idle; nil when executing
```

Add the `Info()` method:

```go
// Info returns a snapshot of the actor's current state for the status API.
func (a *Actor) Info() sockapi.SessionInfo {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()

	info := sockapi.SessionInfo{
		SessionID: a.opts.SessionID,
	}
	if a.taskID != "" {
		info.State = "executing"
		info.TaskID = a.taskID
		info.StartedAt = a.taskStartedAt
	} else {
		info.State = "idle"
		info.IdleSince = a.idleSince
	}
	return info
}
```

Add the import for `sockapi`:

```go
"github.com/gsd-build/daemon/internal/sockapi"
```

Update `executeTask` to set `taskStartedAt` and `idleSince`. In the lock block where `a.taskID` is set:

```go
	a.taskMu.Lock()
	a.taskCancel = cancel
	a.taskID = task.TaskID
	now := time.Now()
	a.taskStartedAt = &now
	a.idleSince = nil
	a.taskMu.Unlock()
```

In the defer block where `a.taskID` is cleared:

```go
	defer func() {
		cancel()
		a.taskMu.Lock()
		a.taskCancel = nil
		a.taskID = ""
		a.taskStartedAt = nil
		idleNow := time.Now()
		a.idleSince = &idleNow
		a.taskMu.Unlock()
	}()
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run TestActorInfoExecutingState -v
```

Expected: PASS

- [ ] **Step 5: Write test for Actor.Info() returning idle state**

```go
// append to test file

func TestActorInfoIdleState(t *testing.T) {
	actor, err := NewActor(Options{
		SessionID:  "sess-2",
		BinaryPath: "echo",
		CWD:        "/tmp",
		Relay:      &mockRelay{},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Actor starts idle — set idleSince.
	actor.taskMu.Lock()
	idleTime := time.Now().Add(-10 * time.Minute)
	actor.idleSince = &idleTime
	actor.taskMu.Unlock()

	info := actor.Info()
	if info.State != "idle" {
		t.Errorf("expected idle, got %s", info.State)
	}
	if info.TaskID != "" {
		t.Errorf("expected empty taskID, got %s", info.TaskID)
	}
	if info.IdleSince == nil {
		t.Error("expected IdleSince to be set")
	}
}
```

- [ ] **Step 6: Run test**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -run TestActorInfoIdleState -v
```

Expected: PASS

- [ ] **Step 7: Add SessionInfos() to Manager**

Add to `internal/session/manager.go`:

```go
// SessionInfos returns a snapshot of all active sessions.
func (m *Manager) SessionInfos() []sockapi.SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	infos := make([]sockapi.SessionInfo, 0, len(m.actors))
	for _, a := range m.actors {
		infos = append(infos, a.Info())
	}
	return infos
}

// ActiveCount returns the number of actors with in-flight tasks.
func (m *Manager) ActiveCount() (total int, executing int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	total = len(m.actors)
	for _, a := range m.actors {
		a.taskMu.Lock()
		if a.taskID != "" {
			executing++
		}
		a.taskMu.Unlock()
	}
	return total, executing
}
```

Add the import:

```go
"github.com/gsd-build/daemon/internal/sockapi"
```

- [ ] **Step 8: Run all session tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/session/ -v
```

Expected: all PASS

- [ ] **Step 9: Commit**

```bash
git add internal/session/actor.go internal/session/manager.go
git commit -m "feat(session): add Info() and SessionInfos() for status API

Actor.Info() returns a SessionInfo snapshot with state, taskID, and
timestamps. Manager.SessionInfos() collects snapshots from all actors.
Manager.ActiveCount() returns total and executing counts."
```

---

### Task 6: Wire socket server into daemon loop

**Files:**
- Modify: `internal/loop/daemon.go`

- [ ] **Step 1: Add StatusProvider implementation to Daemon**

The Daemon struct needs to implement `sockapi.StatusProvider`. Add these fields and methods to `internal/loop/daemon.go`:

Add `startedAt time.Time` field to the `Daemon` struct. Add `relayConnected bool` field with a mutex for thread-safe access.

```go
import (
	// ... existing imports ...
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/gsd-build/daemon/internal/sockapi"
)
```

Add fields to `Daemon`:

```go
type Daemon struct {
	cfg       *config.Config
	version   string
	manager   *session.Manager
	client    *relay.Client
	verbosity display.VerbosityLevel
	startedAt time.Time
	connected atomic.Bool
}
```

Set `startedAt` in `NewWithBinaryPath`:

```go
	return &Daemon{
		cfg:       cfg,
		version:   version,
		manager:   manager,
		client:    client,
		verbosity: verbosity,
		startedAt: time.Now(),
	}, nil
```

Implement the three interface methods:

```go
// Health implements sockapi.StatusProvider.
func (d *Daemon) Health() sockapi.HealthData {
	if d.connected.Load() {
		return sockapi.HealthData{Status: "ok"}
	}
	return sockapi.HealthData{Status: "disconnected"}
}

// Status implements sockapi.StatusProvider.
func (d *Daemon) Status() sockapi.StatusData {
	total, executing := d.manager.ActiveCount()
	return sockapi.StatusData{
		Version:            d.version,
		Uptime:             time.Since(d.startedAt).Truncate(time.Second).String(),
		RelayConnected:     d.connected.Load(),
		RelayURL:           d.cfg.RelayURL,
		MachineID:          d.cfg.MachineID,
		ActiveSessions:     total,
		InFlightTasks:      executing,
		MaxConcurrentTasks: runtime.NumCPU(),
		LogLevel:           "info",
	}
}

// Sessions implements sockapi.StatusProvider.
func (d *Daemon) Sessions() []sockapi.SessionInfo {
	return d.manager.SessionInfos()
}
```

- [ ] **Step 2: Set connected flag in runOnce**

Update `runOnce` to set the connected flag:

```go
func (d *Daemon) runOnce(ctx context.Context) error {
	if _, err := d.client.Connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	d.connected.Store(true)
	fmt.Printf("%srelay connected%s\n", display.Dim, display.Reset)

	connCtx, connCancel := context.WithCancel(ctx)
	go d.runHeartbeat(connCtx)
	go d.runIdleHeartbeat(connCtx)
	go d.runTokenRefreshCheck(connCtx)

	runErr := d.client.Run(ctx)
	d.connected.Store(false)
	connCancel()
	return runErr
}
```

- [ ] **Step 3: Start socket server in Run**

Add socket server startup at the beginning of `Run`, before entering the reconnect loop:

```go
func (d *Daemon) Run(ctx context.Context) error {
	d.client.SetHandler(d.handleMessage)
	d.checkAndRefreshToken()

	// Start Unix socket status API.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("user home: %w", err)
	}
	sockPath := filepath.Join(home, ".gsd-cloud", "daemon.sock")
	sockSrv := sockapi.NewServer(sockPath, d)
	go sockSrv.ListenAndServe(ctx)

	backoff := 1 * time.Second
	const maxBackoff = 60 * time.Second

	for {
		// ... rest unchanged ...
	}
}
```

- [ ] **Step 4: Verify the daemon compiles**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./...
```

Expected: compiles with no errors.

- [ ] **Step 5: Run all tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./...
```

Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/loop/daemon.go
git commit -m "feat(loop): wire Unix socket server into daemon lifecycle

Daemon implements StatusProvider with Health/Status/Sessions methods.
Socket server starts as a goroutine in Run and shuts down with the
daemon context. Connected state tracked via atomic.Bool."
```

---

### Task 7: Upgrade status command to query live daemon

**Files:**
- Modify: `cmd/status.go`

- [ ] **Step 1: Write test for status command output with running daemon**

Since the status command uses cobra and writes to stdout, test the core formatting logic as a standalone function. Add a new test:

```go
// cmd/status_test.go
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
}

func TestFormatStaticStatus(t *testing.T) {
	out := formatStaticStatus("0.1.13", "m-1", "wss://relay.gsd.build/ws/daemon")

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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./cmd/ -run 'TestFormat(Live|Static)Status' -v
```

Expected: FAIL — `formatLiveStatus` and `formatStaticStatus` not defined.

- [ ] **Step 3: Rewrite cmd/status.go**

```go
// cmd/status.go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/sockapi"
	"github.com/spf13/cobra"
)

// socketPath returns the path to the daemon Unix socket.
func socketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gsd-cloud", "daemon.sock"), nil
}

// formatLiveStatus formats output when the daemon is running and reachable.
func formatLiveStatus(s *sockapi.StatusData, sessions []sockapi.SessionInfo) string {
	var b strings.Builder

	statusLabel := "connected"
	if !s.RelayConnected {
		statusLabel = "disconnected (relay unreachable)"
	}

	executing := 0
	idle := 0
	for _, sess := range sessions {
		if sess.State == "executing" {
			executing++
		} else {
			idle++
		}
	}

	fmt.Fprintf(&b, "gsd-cloud v%s\n", s.Version)
	fmt.Fprintf(&b, "status:     %s\n", statusLabel)
	fmt.Fprintf(&b, "relay:      %s\n", s.RelayURL)
	fmt.Fprintf(&b, "uptime:     %s\n", s.Uptime)
	fmt.Fprintf(&b, "sessions:   %d active (%d executing, %d idle)\n", s.ActiveSessions, executing, idle)
	fmt.Fprintf(&b, "tasks:      %d in-flight (max %d)\n", s.InFlightTasks, s.MaxConcurrentTasks)

	return b.String()
}

// formatStaticStatus formats output when the daemon is not running.
func formatStaticStatus(version, machineID, relayURL string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "gsd-cloud v%s\n", version)
	fmt.Fprintf(&b, "status:     not running\n")
	fmt.Fprintf(&b, "machine:    %s\n", machineID)
	fmt.Fprintf(&b, "relay:      %s\n", relayURL)
	fmt.Fprintf(&b, "\nRun 'gsd-cloud start' to connect.\n")

	return b.String()
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		sockPath, err := socketPath()
		if err != nil {
			return fmt.Errorf("socket path: %w", err)
		}

		// Try live daemon first.
		if sockapi.IsDaemonRunning(sockPath) {
			status, err := sockapi.QueryStatus(sockPath)
			if err == nil {
				sessions, _ := sockapi.QuerySessions(sockPath)
				if sessions == nil {
					sessions = []sockapi.SessionInfo{}
				}
				fmt.Print(formatLiveStatus(status, sessions))
				return nil
			}
			// Fall through to static if query failed despite socket existing.
		}

		// Daemon not running — show static config.
		cfg, err := config.Load()
		if err != nil {
			fmt.Println("Not paired.")
			fmt.Println("Run 'gsd-cloud login <code>' to pair.")
			return nil
		}

		fmt.Print(formatStaticStatus(Version, cfg.MachineID, cfg.RelayURL))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./cmd/ -run 'TestFormat(Live|Static)Status' -v
```

Expected: PASS

- [ ] **Step 5: Verify full build**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go build ./...
```

Expected: compiles with no errors.

- [ ] **Step 6: Run all tests in the repo**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./...
```

Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/status.go cmd/status_test.go
git commit -m "feat(cmd): upgrade status command with live daemon queries

gsd-cloud status tries the Unix socket first for live data (uptime,
sessions, relay state). Falls back to static config when the daemon
is not running. Extracted formatting into testable functions."
```

---

### Task 8: Integration test — full socket round-trip

**Files:**
- Create: `internal/sockapi/integration_test.go`

- [ ] **Step 1: Write an integration test that starts a server and queries all three endpoints**

```go
// internal/sockapi/integration_test.go
package sockapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIntegrationFullRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

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

	// Wait for socket
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

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
```

- [ ] **Step 2: Run integration test**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./internal/sockapi/ -run TestIntegrationFullRoundTrip -v
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/sockapi/integration_test.go
git commit -m "test(sockapi): integration test for full socket round-trip

Starts a real Unix socket server, queries all three endpoints and
IsDaemonRunning, verifies cleanup after shutdown."
```

---

### Task 9: Final verification

- [ ] **Step 1: Run all tests**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go test ./...
```

Expected: all PASS

- [ ] **Step 2: Run vet and build**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go vet ./... && go build ./...
```

Expected: no issues.

- [ ] **Step 3: Verify binary runs status without a daemon**

```bash
cd /Users/lexchristopherson/Developer/gsd/gsd-build-daemon
go run . status
```

Expected: either "Not paired" or static config output with "not running" status, depending on whether config.json exists.
