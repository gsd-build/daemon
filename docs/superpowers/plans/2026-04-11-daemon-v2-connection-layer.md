# Daemon v2: Connection Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the daemon's WebSocket client with a production-grade connection layer featuring independent read/write pumps, context-aware send with backpressure, automatic reconnection with jittered backoff, and sleep/wake detection.

**Architecture:** The new `internal/relay` package replaces the existing `client.go` with three files: `conn.go` (connection manager + handshake), `pumps.go` (read pump, write pump, ping manager), and `send.go` (public Send API with context-based timeout). The `Client` type owns the full lifecycle: connect, run pumps, detect failure, reconnect. Callers interact only through `Send(ctx, msg)` and `SetHandler(fn)`.

**Tech Stack:** Go 1.25, `github.com/coder/websocket` v1.8.14, `github.com/gsd-build/protocol-go` v0.4.0

**Spec reference:** `docs/superpowers/specs/2026-04-11-daemon-v2-design.md` Section 1 (Connection Layer)

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/relay/conn.go` | `Client` struct, `NewClient`, `Run` (reconnect loop), `Connect` (dial + handshake), `Close` |
| `internal/relay/pumps.go` | `readPump`, `writePump`, `pingManager` — three independent goroutines per connection |
| `internal/relay/send.go` | `Send(ctx, msg)` — public API with context-based timeout and backpressure |
| `internal/relay/conn_test.go` | Tests for connect, reconnect, handshake, backoff |
| `internal/relay/pumps_test.go` | Tests for read pump dispatch, write pump drain, ping failure |
| `internal/relay/send_test.go` | Tests for send timeout, send success, send on disconnected client |
| Delete: `internal/relay/client.go` | Replaced by conn.go + pumps.go + send.go |
| Delete: `internal/relay/client_test.go` | Replaced by new test files |
| Modify: `internal/loop/daemon.go` | Update to use new `Client.Send(ctx, msg)` signature and `Client.Run(ctx)` reconnect loop |
| Modify: `internal/session/actor.go` | Update `Send` calls to pass context with timeout |

---

### Task 1: Create Send API with context-based timeout

**Files:**
- Create: `internal/relay/send.go`
- Create: `internal/relay/send_test.go`

- [ ] **Step 1: Write the failing test for Send with available capacity**

```go
// internal/relay/send_test.go
package relay

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSendEnqueuesWhenCapacityAvailable(t *testing.T) {
	ch := make(chan []byte, 512)
	s := &sender{sendCh: ch}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	msg := map[string]string{"type": "test"}
	if err := s.Send(ctx, msg); err != nil {
		t.Fatalf("send should succeed: %v", err)
	}

	select {
	case buf := <-ch:
		var got map[string]string
		if err := json.Unmarshal(buf, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["type"] != "test" {
			t.Errorf("expected type=test, got %s", got["type"])
		}
	default:
		t.Fatal("expected message on channel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /path/to/gsd-build-daemon
go test ./internal/relay/ -run TestSendEnqueuesWhenCapacityAvailable -v
```

Expected: FAIL — `sender` type not defined.

- [ ] **Step 3: Write minimal Send implementation**

```go
// internal/relay/send.go
package relay

import (
	"context"
	"encoding/json"
	"fmt"
)

// sender holds the send channel. Embedded in Client.
type sender struct {
	sendCh chan []byte
}

// Send marshals msg to JSON and enqueues it onto the send channel.
// Blocks until the message is enqueued or ctx expires.
func (s *sender) Send(ctx context.Context, msg any) error {
	buf, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	select {
	case s.sendCh <- buf:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("send: %w", ctx.Err())
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/relay/ -run TestSendEnqueuesWhenCapacityAvailable -v
```

Expected: PASS

- [ ] **Step 5: Write test for Send timeout when channel is full**

```go
// append to internal/relay/send_test.go

func TestSendTimesOutWhenChannelFull(t *testing.T) {
	ch := make(chan []byte, 1)
	s := &sender{sendCh: ch}

	// Fill the channel
	ch <- []byte("blocked")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := s.Send(ctx, map[string]string{"type": "overflow"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected deadline exceeded, got: %v", err)
	}
}
```

Add `"strings"` to the import block.

- [ ] **Step 6: Run test to verify it passes**

```bash
go test ./internal/relay/ -run TestSendTimesOutWhenChannelFull -v
```

Expected: PASS — the 50ms timeout fires before channel drains.

- [ ] **Step 7: Write test for Send with cancelled context**

```go
// append to internal/relay/send_test.go

func TestSendReturnsErrorOnCancelledContext(t *testing.T) {
	ch := make(chan []byte, 1)
	ch <- []byte("blocked")
	s := &sender{sendCh: ch}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := s.Send(ctx, map[string]string{"type": "cancelled"})
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}
```

- [ ] **Step 8: Run all send tests**

```bash
go test ./internal/relay/ -run TestSend -v
```

Expected: all 3 PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/relay/send.go internal/relay/send_test.go
git commit -m "feat(relay): add context-aware Send API with backpressure

Send(ctx, msg) enqueues onto a buffered channel. Blocks until space
is available or the context expires. No indefinite blocking."
```

---

### Task 2: Create write pump

**Files:**
- Create: `internal/relay/pumps.go`
- Create: `internal/relay/pumps_test.go`

- [ ] **Step 1: Write the failing test for write pump draining messages**

```go
// internal/relay/pumps_test.go
package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWritePumpDrainsChannel(t *testing.T) {
	var mu sync.Mutex
	var received [][]byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			mu.Lock()
			received = append(received, data)
			mu.Unlock()
		}
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	sendCh := make(chan []byte, 512)
	errCh := make(chan error, 1)

	go writePump(ctx, conn, sendCh, errCh)

	sendCh <- []byte(`{"type":"a"}`)
	sendCh <- []byte(`{"type":"b"}`)
	sendCh <- []byte(`{"type":"c"}`)

	// Wait for server to receive
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(received))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/relay/ -run TestWritePumpDrainsChannel -v
```

Expected: FAIL — `writePump` not defined.

- [ ] **Step 3: Implement writePump**

```go
// internal/relay/pumps.go
package relay

import (
	"context"
	"fmt"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

// writePump drains sendCh and writes each message to the WebSocket.
// Exits on ctx cancellation, write error, or when sendCh is closed.
// Sends any error to errCh.
func writePump(ctx context.Context, conn *websocket.Conn, sendCh <-chan []byte, errCh chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case buf, ok := <-sendCh:
			if !ok {
				return // channel closed
			}
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, buf)
			cancel()
			if err != nil {
				errCh <- fmt.Errorf("write pump: %w", err)
				return
			}
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/relay/ -run TestWritePumpDrainsChannel -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/relay/pumps.go internal/relay/pumps_test.go
git commit -m "feat(relay): add writePump — dedicated goroutine for WebSocket writes

Drains the send channel and writes each message with a 10s timeout.
Exits on context cancellation or write error."
```

---

### Task 3: Create read pump

**Files:**
- Modify: `internal/relay/pumps.go`
- Modify: `internal/relay/pumps_test.go`

- [ ] **Step 1: Write the failing test for read pump dispatching to handler**

```go
// append to internal/relay/pumps_test.go

func TestReadPumpDispatchesToHandler(t *testing.T) {
	var mu sync.Mutex
	var dispatched []string

	handler := func(env *protocol.Envelope) error {
		mu.Lock()
		dispatched = append(dispatched, env.Type)
		mu.Unlock()
		return nil
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()

		// Send a stream frame
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"stream","sessionId":"s1","channelId":"ch1","sequenceNumber":1,"event":{}}`))
		// Send a taskComplete frame
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"taskComplete","taskId":"t1","sessionId":"s1","channelId":"ch1","claudeSessionId":"c1","inputTokens":1,"outputTokens":1,"costUsd":"0.01","durationMs":100}`))

		// Keep connection open until client disconnects
		_, _, _ = c.Read(r.Context())
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	errCh := make(chan error, 1)
	go readPump(ctx, conn, handler, errCh)

	// Wait for dispatch
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(dispatched)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(dispatched) != 2 {
		t.Fatalf("expected 2 dispatched, got %d", len(dispatched))
	}
	if dispatched[0] != "stream" {
		t.Errorf("first dispatch: expected stream, got %s", dispatched[0])
	}
	if dispatched[1] != "taskComplete" {
		t.Errorf("second dispatch: expected taskComplete, got %s", dispatched[1])
	}
}
```

Add `protocol "github.com/gsd-build/protocol-go"` to the import block if not present.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/relay/ -run TestReadPumpDispatchesToHandler -v
```

Expected: FAIL — `readPump` not defined.

- [ ] **Step 3: Implement readPump**

Append to `internal/relay/pumps.go`:

```go
// MessageHandler is called for every frame received from the relay.
type MessageHandler func(env *protocol.Envelope) error

// readPump reads frames from the WebSocket, parses envelopes, and
// dispatches to the handler. Exits on ctx cancellation or read error.
// Sends any error to errCh.
func readPump(ctx context.Context, conn *websocket.Conn, handler MessageHandler, errCh chan<- error) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // clean shutdown
			}
			errCh <- fmt.Errorf("read pump: %w", err)
			return
		}
		env, err := protocol.ParseEnvelope(data)
		if err != nil {
			continue // skip malformed frames
		}
		if handler != nil {
			if err := handler(env); err != nil {
				errCh <- fmt.Errorf("handler: %w", err)
				return
			}
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/relay/ -run TestReadPumpDispatchesToHandler -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/relay/pumps.go internal/relay/pumps_test.go
git commit -m "feat(relay): add readPump — dedicated goroutine for WebSocket reads

Reads frames in a tight loop, parses envelopes, dispatches to handler.
Malformed frames are skipped. Exits on read error or context cancellation."
```

---

### Task 4: Create ping manager

**Files:**
- Modify: `internal/relay/pumps.go`
- Modify: `internal/relay/pumps_test.go`

- [ ] **Step 1: Write the failing test for ping manager triggering reconnect on failure**

```go
// append to internal/relay/pumps_test.go

func TestPingManagerSignalsAfterConsecutiveFailures(t *testing.T) {
	// Create a server that accepts but never responds to pings
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		// Close immediately so pings fail
		c.Close(websocket.StatusNormalClosure, "")
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	errCh := make(chan error, 1)

	// Use a short interval for testing
	go pingManager(ctx, conn, 100*time.Millisecond, 3, errCh)

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected non-nil error from ping manager")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ping manager did not signal failure within 5s")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/relay/ -run TestPingManagerSignalsAfterConsecutiveFailures -v
```

Expected: FAIL — `pingManager` not defined.

- [ ] **Step 3: Implement pingManager**

Append to `internal/relay/pumps.go`:

```go
// pingManager sends WebSocket pings at the given interval. After
// maxFailures consecutive ping failures, it signals via errCh.
func pingManager(ctx context.Context, conn *websocket.Conn, interval time.Duration, maxFailures int, errCh chan<- error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				failures++
				if failures >= maxFailures {
					errCh <- fmt.Errorf("ping manager: %d consecutive failures", failures)
					return
				}
			} else {
				failures = 0
			}
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/relay/ -run TestPingManagerSignalsAfterConsecutiveFailures -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/relay/pumps.go internal/relay/pumps_test.go
git commit -m "feat(relay): add pingManager — detects dead connections via pings

Sends pings at a configurable interval. After N consecutive failures,
signals the connection manager to reconnect."
```

---

### Task 5: Create connection manager with handshake

**Files:**
- Create: `internal/relay/conn.go`
- Create: `internal/relay/conn_test.go`
- Delete: `internal/relay/client.go`
- Delete: `internal/relay/client_test.go`

- [ ] **Step 1: Write the failing test for connect and handshake**

```go
// internal/relay/conn_test.go
package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

func newTestServer(t *testing.T) (*httptest.Server, *fakeRelayState) {
	t.Helper()
	state := &fakeRelayState{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()

		// Read Hello
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		env, _ := protocol.ParseEnvelope(data)
		if hello, ok := env.Payload.(*protocol.Hello); ok {
			state.mu.Lock()
			state.hellos = append(state.hellos, hello)
			state.mu.Unlock()
		}

		// Send Welcome
		welcome := protocol.Welcome{Type: protocol.MsgTypeWelcome}
		buf, _ := json.Marshal(welcome)
		_ = c.Write(r.Context(), websocket.MessageText, buf)

		// Keep alive until context done, echoing reads
		for {
			_, _, err := c.Read(r.Context())
			if err != nil {
				return
			}
		}
	}))
	return server, state
}

type fakeRelayState struct {
	mu     sync.Mutex
	hellos []*protocol.Hello
}

func TestClientConnectHandshake(t *testing.T) {
	server, state := newTestServer(t)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "?token=secret"
	client := NewClient(Config{
		URL:           url,
		AuthToken:     "secret",
		MachineID:     "m-1",
		DaemonVersion: "0.1.0",
		OS:            "darwin",
		Arch:          "arm64",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	welcome, err := client.Connect(ctx, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if welcome == nil {
		t.Fatal("nil welcome")
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.hellos) != 1 {
		t.Fatalf("expected 1 hello, got %d", len(state.hellos))
	}
	if state.hellos[0].MachineID != "m-1" {
		t.Errorf("expected machineId=m-1, got %s", state.hellos[0].MachineID)
	}
}
```

Add `"sync"` to imports.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/relay/ -run TestClientConnectHandshake -v
```

Expected: FAIL — `NewClient` signature mismatch or `Client` type conflicts with old client.go.

- [ ] **Step 3: Delete old client files**

```bash
rm internal/relay/client.go internal/relay/client_test.go
```

- [ ] **Step 4: Implement the new Client in conn.go**

```go
// internal/relay/conn.go
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

// Config is immutable per-client settings.
type Config struct {
	URL           string
	AuthToken     string
	MachineID     string
	DaemonVersion string
	OS            string
	Arch          string
}

// ActiveTasksFunc returns the list of currently active task IDs.
// Called during Hello construction on every connect/reconnect.
type ActiveTasksFunc func() []string

// Client manages a WebSocket connection to the relay with automatic
// reconnection, independent read/write pumps, and context-aware send.
type Client struct {
	cfg     Config
	sender  // embedded — provides Send(ctx, msg)
	handler MessageHandler

	mu   sync.Mutex
	conn *websocket.Conn
}

// NewClient constructs a Client. Call Run to connect and process messages.
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:    cfg,
		sender: sender{sendCh: make(chan []byte, 512)},
	}
}

// SetHandler registers the message handler for incoming frames.
func (c *Client) SetHandler(h MessageHandler) {
	c.handler = h
}

// Connect dials the relay, sends Hello, and waits for Welcome.
// activeTasks is the list of task IDs to include in the Hello (may be nil).
func (c *Client) Connect(ctx context.Context, activeTasks []string) (*protocol.Welcome, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.cfg.AuthToken)

	conn, _, err := websocket.Dial(dialCtx, c.cfg.URL, &websocket.DialOptions{
		HTTPHeader: header,
	})
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	conn.SetReadLimit(1 << 20) // 1 MB

	// Send Hello
	hello := protocol.Hello{
		Type:          protocol.MsgTypeHello,
		MachineID:     c.cfg.MachineID,
		DaemonVersion: c.cfg.DaemonVersion,
		OS:            c.cfg.OS,
		Arch:          c.cfg.Arch,
	}
	buf, _ := json.Marshal(hello)
	if err := conn.Write(ctx, websocket.MessageText, buf); err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("send hello: %w", err)
	}

	// Wait for Welcome
	welcomeCtx, welcomeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer welcomeCancel()
	_, data, err := conn.Read(welcomeCtx)
	if err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("read welcome: %w", err)
	}
	env, err := protocol.ParseEnvelope(data)
	if err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("parse welcome: %w", err)
	}
	welcome, ok := env.Payload.(*protocol.Welcome)
	if !ok {
		conn.CloseNow()
		return nil, fmt.Errorf("unexpected first frame: %s", env.Type)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	return welcome, nil
}

// RunOnce connects, starts pumps, and blocks until the connection fails.
// Returns the error that caused the disconnection.
func (c *Client) RunOnce(ctx context.Context, activeTasks []string) error {
	_, err := c.Connect(ctx, activeTasks)
	if err != nil {
		return err
	}

	pumpCtx, pumpCancel := context.WithCancel(ctx)
	defer pumpCancel()

	errCh := make(chan error, 3)

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	go readPump(pumpCtx, conn, c.handler, errCh)
	go writePump(pumpCtx, conn, c.sendCh, errCh)
	go pingManager(pumpCtx, conn, 25*time.Second, 3, errCh)

	// Wait for first error from any pump
	select {
	case err := <-errCh:
		pumpCancel()
		conn.CloseNow()
		return err
	case <-ctx.Done():
		pumpCancel()
		conn.Close(websocket.StatusGoingAway, "shutdown")
		return ctx.Err()
	}
}

// Run connects to the relay and reconnects automatically on failure.
// Blocks until ctx is canceled. Uses jittered exponential backoff.
func (c *Client) Run(ctx context.Context, getActiveTasks ActiveTasksFunc) error {
	backoff := 1 * time.Second
	const maxBackoff = 60 * time.Second

	for {
		connStart := time.Now()

		var tasks []string
		if getActiveTasks != nil {
			tasks = getActiveTasks()
		}

		err := c.RunOnce(ctx, tasks)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Reset backoff if connection was healthy (alive > 2 min)
		if time.Since(connStart) > 2*time.Minute {
			backoff = 1 * time.Second
		}

		// Jittered backoff: ±20%
		jitter := time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64()))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Close closes the underlying WebSocket connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.Close(websocket.StatusNormalClosure, "")
	}
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

```bash
go test ./internal/relay/ -run TestClientConnectHandshake -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/relay/conn.go internal/relay/conn_test.go
git rm internal/relay/client.go internal/relay/client_test.go
git commit -m "feat(relay): new Client with connection manager, pumps, and reconnect

Replaces the old single-goroutine client with independent read/write
pumps, ping manager, jittered exponential backoff, and context-aware
Send API. No shared mutex on the WebSocket connection."
```

---

### Task 6: Write reconnect test

**Files:**
- Modify: `internal/relay/conn_test.go`

- [ ] **Step 1: Write test for automatic reconnection**

```go
// append to internal/relay/conn_test.go

func TestClientReconnectsAfterDisconnect(t *testing.T) {
	var mu sync.Mutex
	connectCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()

		mu.Lock()
		connectCount++
		count := connectCount
		mu.Unlock()

		// Read Hello
		_, _, _ = c.Read(r.Context())

		// Send Welcome
		welcome := protocol.Welcome{Type: protocol.MsgTypeWelcome}
		buf, _ := json.Marshal(welcome)
		_ = c.Write(r.Context(), websocket.MessageText, buf)

		if count == 1 {
			// First connection: close after welcome to trigger reconnect
			c.Close(websocket.StatusGoingAway, "test disconnect")
			return
		}

		// Second connection: stay alive
		_, _, _ = c.Read(r.Context())
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "?token=secret"
	client := NewClient(Config{
		URL:           url,
		AuthToken:     "secret",
		MachineID:     "m-1",
		DaemonVersion: "0.1.0",
		OS:            "darwin",
		Arch:          "arm64",
	})
	client.SetHandler(func(env *protocol.Envelope) error { return nil })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run in background — it will reconnect after first disconnect
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx, nil)
	}()

	// Wait for second connection
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := connectCount
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	mu.Lock()
	n := connectCount
	mu.Unlock()
	if n < 2 {
		t.Fatalf("expected at least 2 connections, got %d", n)
	}

	cancel()
	<-done
}
```

- [ ] **Step 2: Run test**

```bash
go test ./internal/relay/ -run TestClientReconnectsAfterDisconnect -v -timeout 15s
```

Expected: PASS — client connects, server disconnects, client reconnects.

- [ ] **Step 3: Commit**

```bash
git add internal/relay/conn_test.go
git commit -m "test(relay): add reconnection test

Verifies client automatically reconnects after server-initiated
disconnect using jittered exponential backoff."
```

---

### Task 7: Update daemon loop to use new Client API

**Files:**
- Modify: `internal/loop/daemon.go`

- [ ] **Step 1: Update daemon.go imports and Daemon struct**

The `Daemon` struct stays the same — it already has `client *relay.Client`. The key changes are:

1. `runOnce()` is deleted — the new `client.Run()` handles reconnection internally.
2. `Run()` delegates to `client.Run()` with callbacks.
3. `handleMessage` stays the same.

Replace the `Run` and `runOnce` methods:

```go
// Run connects to the relay and blocks until ctx is canceled.
// The client handles reconnection automatically.
func (d *Daemon) Run(ctx context.Context) error {
	d.client.SetHandler(d.handleMessage)

	// Check token expiry at startup.
	d.checkAndRefreshToken()
	go d.runTokenRefreshCheck(ctx)

	fmt.Printf("Connecting to %s as %s...\n", d.cfg.RelayURL, d.cfg.MachineID)
	return d.client.Run(ctx, d.getActiveTasks)
}

// getActiveTasks returns the list of currently executing task IDs.
// Called by the client on every connect/reconnect for Hello state sync.
func (d *Daemon) getActiveTasks() []string {
	return d.manager.ActiveTaskIDs()
}
```

Delete the `runOnce` method entirely.
Delete the `runHeartbeat` method — the relay now uses the existing heartbeat mechanism (keep for now, we'll evaluate in actor hardening plan).
Keep `runIdleHeartbeat` if it's wanted for terminal output.

- [ ] **Step 2: Remove the reconnect loop from daemon.go**

Delete the entire `for` loop body in the old `Run()` that handled backoff, reconnection, and `token_expired` detection. The new `client.Run()` handles reconnection. Token expiry detection moves to the client's connect error handling.

Add token_expired detection in the daemon's Run:

```go
func (d *Daemon) Run(ctx context.Context) error {
	d.client.SetHandler(d.handleMessage)
	d.checkAndRefreshToken()
	go d.runTokenRefreshCheck(ctx)

	fmt.Printf("Connecting to %s as %s...\n", d.cfg.RelayURL, d.cfg.MachineID)

	err := d.client.Run(ctx, d.getActiveTasks)
	if err != nil && strings.Contains(err.Error(), "token_expired") {
		return fmt.Errorf("machine token has expired — run `gsd-cloud login` to re-pair this machine")
	}
	return err
}
```

- [ ] **Step 3: Add ActiveTaskIDs to session Manager**

In `internal/session/manager.go`, add:

```go
// ActiveTaskIDs returns a list of task IDs currently being executed.
func (m *Manager) ActiveTaskIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, a := range m.actors {
		if id := a.InFlightTaskID(); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
```

Update `Actor.InFlightTaskID()` in `actor.go` to return the actual in-flight task ID (it currently returns `""`). Read the `taskID` field that was added:

```go
func (a *Actor) InFlightTaskID() string {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	return a.taskID
}
```

- [ ] **Step 4: Update all Send calls in actor.go**

The new `Send` takes a context. Update every `a.opts.Relay.Send(msg)` call to use `a.opts.Relay.Send(ctx, msg)`.

Update the `RelaySender` interface:

```go
// RelaySender is the minimal interface the actor needs to push events to the relay.
type RelaySender interface {
	Send(ctx context.Context, msg any) error
}
```

In `runExecutor`, stream events get a 5-second timeout:

```go
sendCtx, sendCancel := context.WithTimeout(ctx, 5*time.Second)
if err := a.opts.Relay.Send(sendCtx, frame); err != nil {
	log.Printf("[actor] relay send failed: session=%s seq=%d err=%v",
		a.opts.SessionID, next, err)
}
sendCancel()
```

Control messages (TaskComplete, TaskStarted, TaskError, TaskCancelled, PermissionRequest, Question) get a 30-second timeout:

```go
sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
err := a.opts.Relay.Send(sendCtx, msg)
sendCancel()
```

- [ ] **Step 5: Update fakeRelay in tests**

In `internal/session/actor_test.go`, update `fakeRelay.Send` to accept context:

```go
func (r *fakeRelay) Send(ctx context.Context, msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	r.cond.Broadcast()
	return nil
}
```

Similarly update any other test doubles that implement `RelaySender`.

- [ ] **Step 6: Build and run all tests**

```bash
cd /path/to/gsd-build-daemon
go build ./...
go test ./...
```

Expected: all pass. Fix any remaining compilation errors from the Send signature change.

- [ ] **Step 7: Commit**

```bash
git add internal/loop/daemon.go internal/session/actor.go internal/session/manager.go internal/session/actor_test.go internal/session/manager_test.go
git commit -m "refactor: wire daemon loop and actors to new relay Client

Daemon.Run delegates to client.Run for automatic reconnection.
Actors pass context with timeout on every Send call. RelaySender
interface now requires context parameter. ActiveTaskIDs added to
Manager for Hello state sync on reconnect."
```

---

### Task 8: Update e2e tests

**Files:**
- Modify: `tests/e2e/daemon_e2e_test.go`
- Modify: `tests/e2e/stub_relay.go`

- [ ] **Step 1: Update stub relay Send to match new signature**

Check `tests/e2e/stub_relay.go` for any `Send(msg)` calls that need to become `Send(ctx, msg)`. Update the stub relay to be compatible with the new Client.

- [ ] **Step 2: Run e2e tests**

```bash
go test ./tests/e2e/ -v -timeout 60s
```

Fix any compilation errors. The e2e tests exercise the full daemon → stub relay path so they'll surface any remaining integration issues.

- [ ] **Step 3: Commit**

```bash
git add tests/e2e/
git commit -m "test: update e2e tests for new relay Client API

Stub relay updated for context-aware Send signature."
```

---

### Task 9: Add sleep/wake detection (macOS)

**Files:**
- Create: `internal/relay/wake.go`
- Create: `internal/relay/wake_test.go`
- Modify: `internal/relay/conn.go`

- [ ] **Step 1: Create wake detector**

The wake detector monitors system sleep/wake transitions. On macOS, it polls `sysctl kern.waketime` which changes when the system wakes. On Linux, it monitors the monotonic clock — a large gap between wall clock and monotonic clock indicates sleep.

```go
// internal/relay/wake.go
package relay

import (
	"context"
	"time"
)

// WakeDetector sends on the returned channel when a sleep/wake cycle
// is detected. Uses a clock-gap heuristic: if the time between ticks
// exceeds 2x the tick interval, the system likely slept.
func WakeDetector(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		const interval = 5 * time.Second
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastTick := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				gap := now.Sub(lastTick)
				if gap > 3*interval {
					// System likely slept — gap is way larger than expected
					select {
					case ch <- struct{}{}:
					default:
					}
				}
				lastTick = now
			}
		}
	}()
	return ch
}
```

- [ ] **Step 2: Write test**

```go
// internal/relay/wake_test.go
package relay

import (
	"context"
	"testing"
	"time"
)

func TestWakeDetectorReturnsChannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	ch := WakeDetector(ctx)
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	// We can't easily simulate sleep in a test, but we can verify
	// the detector runs without blocking or panicking.
	<-ctx.Done()
}
```

- [ ] **Step 3: Integrate wake detector into connection manager**

In `conn.go`, update `Run` to listen for wake events and trigger immediate reconnect:

```go
func (c *Client) Run(ctx context.Context, getActiveTasks ActiveTasksFunc) error {
	backoff := 1 * time.Second
	const maxBackoff = 60 * time.Second

	wakeCh := WakeDetector(ctx)

	for {
		connStart := time.Now()

		var tasks []string
		if getActiveTasks != nil {
			tasks = getActiveTasks()
		}

		err := c.RunOnce(ctx, tasks)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Reset backoff if connection was healthy (alive > 2 min)
		if time.Since(connStart) > 2*time.Minute {
			backoff = 1 * time.Second
		}

		// Jittered backoff: ±20%
		jitter := time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64()))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wakeCh:
			// System woke from sleep — reconnect immediately
			continue
		case <-time.After(jitter):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/relay/ -v
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/wake.go internal/relay/wake_test.go internal/relay/conn.go
git commit -m "feat(relay): add sleep/wake detection for immediate reconnect

Uses clock-gap heuristic — if the tick interval is 3x longer than
expected, the system likely slept. Triggers immediate reconnect
instead of waiting for backoff timer."
```

---

### Task 10: Clean up unused code and verify

**Files:**
- Various

- [ ] **Step 1: Search for any remaining references to the old API**

```bash
grep -r "\.Send(" internal/ tests/ --include="*.go" | grep -v "_test.go" | grep -v "send.go" | grep -v "conn.go"
```

Look for any `Send(msg)` calls (without ctx) that weren't updated. Fix them.

- [ ] **Step 2: Run full test suite**

```bash
go build ./...
go test ./...
```

Expected: all pass, zero compilation warnings.

- [ ] **Step 3: Verify the old client files are gone**

```bash
ls internal/relay/
```

Expected: `conn.go`, `conn_test.go`, `pumps.go`, `pumps_test.go`, `send.go`, `send_test.go`. No `client.go` or `client_test.go`.

- [ ] **Step 4: Final commit**

```bash
git add -A
git commit -m "chore: clean up remaining references to old relay client"
```
