package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

// StubRelay is an in-process fake relay for daemon integration tests.
// It upgrades a WebSocket, records received frames, and lets the test
// enqueue frames to send to the connected daemon.
type StubRelay struct {
	t *testing.T

	server *httptest.Server

	mu        sync.Mutex
	received  []*protocol.Envelope
	sendQueue [][]byte
	connCh    chan struct{}
	conn      *websocket.Conn
}

// NewStubRelay starts a stub relay on a random local port.
func NewStubRelay(t *testing.T) *StubRelay {
	t.Helper()
	s := &StubRelay{
		t:      t,
		connCh: make(chan struct{}, 1),
	}

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "missing Authorization header", http.StatusUnauthorized)
			return
		}

		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("stub relay: websocket accept error: %v", err)
			return
		}
		defer c.CloseNow()

		s.mu.Lock()
		s.conn = c
		s.mu.Unlock()

		select {
		case s.connCh <- struct{}{}:
		default:
		}

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		go func() {
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.mu.Lock()
					queued := s.sendQueue
					s.sendQueue = nil
					s.mu.Unlock()
					for _, data := range queued {
						wctx, wcancel := context.WithTimeout(ctx, 2*time.Second)
						if err := c.Write(wctx, websocket.MessageText, data); err != nil {
							wcancel()
							t.Logf("stub relay: write error: %v", err)
							return
						}
						wcancel()
					}
				}
			}
		}()

		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			env, err := protocol.ParseEnvelope(data)
			if err != nil {
				t.Logf("stub relay: parse envelope error: %v", err)
				continue
			}
			s.mu.Lock()
			s.received = append(s.received, env)
			s.mu.Unlock()
		}
	}))

	t.Cleanup(func() { s.Close() })

	return s
}

// URL returns the ws:// form of the stub relay's HTTP URL.
func (s *StubRelay) URL() string {
	return strings.Replace(s.server.URL, "http", "ws", 1)
}

// WaitForConnection blocks until a daemon connects, or returns an error on timeout.
func (s *StubRelay) WaitForConnection(timeout time.Duration) error {
	select {
	case <-s.connCh:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("stub relay: timeout waiting for daemon connection")
	}
}

// Send enqueues a payload to be written to the connected daemon. The payload
// is marshaled with encoding/json and sent as a single text frame; callers
// pass concrete protocol message structs (e.g. *protocol.Welcome) directly.
func (s *StubRelay) Send(payload any) error {
	if payload == nil {
		return fmt.Errorf("stub relay: nil payload")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("stub relay: marshal payload: %w", err)
	}
	s.mu.Lock()
	s.sendQueue = append(s.sendQueue, data)
	s.mu.Unlock()
	return nil
}

// Received returns a snapshot of all envelopes received from the daemon.
func (s *StubRelay) Received() []*protocol.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*protocol.Envelope, len(s.received))
	copy(out, s.received)
	return out
}

// WaitForFrame polls the received buffer for a frame matching msgType, returning
// it as soon as it appears or an error on timeout.
func (s *StubRelay) WaitForFrame(msgType string, timeout time.Duration) (*protocol.Envelope, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		frames := s.Received()
		for _, env := range frames {
			if env.Type == msgType {
				return env, nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, fmt.Errorf("stub relay: timeout waiting for frame type %q (received %d frames)", msgType, len(s.Received()))
}

// Close shuts down the underlying httptest server.
func (s *StubRelay) Close() {
	s.server.Close()
}
