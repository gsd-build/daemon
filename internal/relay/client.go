// Package relay implements the WebSocket client the daemon uses to
// talk to the Fly.io relay.
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-cloud/protocol-go"
)

// Config is immutable per-connection settings.
type Config struct {
	URL           string
	AuthToken     string
	MachineID     string
	DaemonVersion string
	OS            string
	Arch          string
}

// MessageHandler is called for every frame received from the relay.
type MessageHandler func(env *protocol.Envelope) error

// Client is a WebSocket client with send/receive support and auto-reconnect.
type Client struct {
	cfg Config

	mu      sync.Mutex
	conn    *websocket.Conn
	handler MessageHandler
	sendCh  chan []byte
	closed  bool
}

// NewClient constructs a Client. Call Connect to actually dial.
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:    cfg,
		sendCh: make(chan []byte, 256),
	}
}

// SetHandler registers the message handler.
func (c *Client) SetHandler(h MessageHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handler = h
}

// Connect dials the relay, sends a Hello, and waits for Welcome.
// Returns the Welcome payload on success. After Connect returns successfully,
// the caller should call Run in a goroutine to process incoming messages.
func (c *Client) Connect(
	ctx context.Context,
	lastSequences map[string]int64,
) (*protocol.Welcome, error) {
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

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	// Send Hello
	hello := protocol.Hello{
		Type:                  protocol.MsgTypeHello,
		MachineID:             c.cfg.MachineID,
		DaemonVersion:         c.cfg.DaemonVersion,
		OS:                    c.cfg.OS,
		Arch:                  c.cfg.Arch,
		LastSequenceBySession: lastSequences,
	}
	buf, _ := json.Marshal(hello)
	if err := conn.Write(ctx, websocket.MessageText, buf); err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("send hello: %w", err)
	}

	// Wait for Welcome
	_, data, err := conn.Read(ctx)
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

	return welcome, nil
}

// Send queues a message to be written to the WebSocket.
func (c *Client) Send(msg any) error {
	buf, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	select {
	case c.sendCh <- buf:
		return nil
	default:
		return fmt.Errorf("send queue full")
	}
}

// Run reads from the connection and writes queued outbound messages.
// Blocks until ctx is canceled or the connection is closed.
func (c *Client) Run(ctx context.Context) error {
	errCh := make(chan error, 2)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case buf := <-c.sendCh:
				c.mu.Lock()
				conn := c.conn
				c.mu.Unlock()
				if conn == nil {
					errCh <- fmt.Errorf("not connected")
					return
				}
				if err := conn.Write(ctx, websocket.MessageText, buf); err != nil {
					errCh <- fmt.Errorf("write: %w", err)
					return
				}
			}
		}
	}()

	go func() {
		for {
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()
			if conn == nil {
				errCh <- fmt.Errorf("not connected")
				return
			}
			_, data, err := conn.Read(ctx)
			if err != nil {
				errCh <- fmt.Errorf("read: %w", err)
				return
			}
			env, err := protocol.ParseEnvelope(data)
			if err != nil {
				continue // skip malformed frames
			}
			c.mu.Lock()
			handler := c.handler
			c.mu.Unlock()
			if handler != nil {
				if err := handler(env); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	return <-errCh
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.conn != nil {
		return c.conn.Close(websocket.StatusNormalClosure, "")
	}
	return nil
}
