// Package relay implements the WebSocket client the daemon uses to
// talk to the Fly.io relay.
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

const (
	keepalivePingInterval = 10 * time.Second
	keepaliveMaxFailures  = 2
)

// Config contains the per-client connection settings.
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
	cfg       Config
	sender    // embedded — provides Send(ctx, msg)
	handler   MessageHandler
	onConnect func(context.Context) error

	mu   sync.Mutex
	conn *websocket.Conn

	connected atomic.Bool
}

// NewClient constructs a Client. Call Run to connect and process messages.
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:    cfg,
		sender: sender{sendCh: make(chan []byte, 512)},
	}
}

// SetHandler registers the message handler for incoming frames.
// Must be called before Run.
func (c *Client) SetHandler(h MessageHandler) {
	c.handler = h
}

// SetOnConnect registers a hook that runs after each successful welcome.
func (c *Client) SetOnConnect(fn func(context.Context) error) {
	c.onConnect = fn
}

// SetAuthToken updates the bearer token used for future relay connections.
func (c *Client) SetAuthToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.AuthToken = token
}

// Connect dials the relay, sends Hello, and waits for Welcome.
// activeTasks is the list of task IDs to include in the Hello (may be nil).
func (c *Client) Connect(ctx context.Context, activeTasks []string) (*protocol.Welcome, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	c.mu.Lock()
	authToken := c.cfg.AuthToken
	url := c.cfg.URL
	machineID := c.cfg.MachineID
	daemonVersion := c.cfg.DaemonVersion
	osName := c.cfg.OS
	arch := c.cfg.Arch
	c.mu.Unlock()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+authToken)

	conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{
		HTTPHeader: header,
	})
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	conn.SetReadLimit(1 << 20) // 1 MB

	// Send Hello
	hello := protocol.Hello{
		Type:          protocol.MsgTypeHello,
		MachineID:     machineID,
		DaemonVersion: daemonVersion,
		OS:            osName,
		Arch:          arch,
		ActiveTasks:   activeTasks,
	}
	buf, err := json.Marshal(hello)
	if err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("marshal hello: %w", err)
	}
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
		c.connected.Store(false)
		return err
	}
	c.connected.Store(true)
	if c.onConnect != nil {
		if err := c.onConnect(ctx); err != nil {
			c.mu.Lock()
			if c.conn != nil {
				c.conn.CloseNow()
				c.conn = nil
			}
			c.mu.Unlock()
			c.connected.Store(false)
			return fmt.Errorf("on connect: %w", err)
		}
	}

	pumpCtx, pumpCancel := context.WithCancel(ctx)
	defer pumpCancel()

	errCh := make(chan error, 3)

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	go readPump(pumpCtx, conn, c.handler, errCh)
	go writePump(pumpCtx, conn, c.sendCh, errCh)
	go pingManager(pumpCtx, conn, keepalivePingInterval, keepaliveMaxFailures, errCh)

	slog.Info("relay connected, pumps started")

	// Wait for first error from any pump
	select {
	case err := <-errCh:
		slog.Warn("pump error, disconnecting", "err", err)
		c.connected.Store(false)
		pumpCancel()
		conn.CloseNow()
		return err
	case <-ctx.Done():
		slog.Info("context cancelled, closing connection")
		c.connected.Store(false)
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

		connDuration := time.Since(connStart).Truncate(time.Second)
		slog.Warn("relay disconnected", "duration", connDuration, "err", err)

		// Reset backoff if connection was healthy (alive > 2 min)
		if connDuration > 2*time.Minute {
			backoff = 1 * time.Second
		}

		// Jittered backoff: ±20%
		jitter := time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64()))
		slog.Info("reconnecting", "backoff", jitter.Truncate(time.Millisecond))

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

		_ = err // logged by caller; we just reconnect
	}
}

// Close closes the underlying WebSocket connection.
func (c *Client) Close() error {
	c.connected.Store(false)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.Close(websocket.StatusNormalClosure, "")
	}
	return nil
}

// Connected reports whether the client currently has an active relay connection.
func (c *Client) Connected() bool {
	return c.connected.Load()
}

// SetConnectedForTest forces connection state in tests.
func (c *Client) SetConnectedForTest(connected bool) {
	c.connected.Store(connected)
}

// DrainQueuedForTest returns the next queued outbound message without needing
// an active websocket connection.
func (c *Client) DrainQueuedForTest(ctx context.Context) (*protocol.Envelope, error) {
	return c.sender.drainQueued(ctx)
}
