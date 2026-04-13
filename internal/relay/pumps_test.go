package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
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

func TestReadPumpContinuesAfterHandlerError(t *testing.T) {
	var mu sync.Mutex
	var dispatched []string

	handlerCalls := 0
	handler := func(env *protocol.Envelope) error {
		mu.Lock()
		dispatched = append(dispatched, env.Type)
		mu.Unlock()
		handlerCalls++
		if handlerCalls == 1 {
			return errors.New("synthetic handler failure")
		}
		return nil
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()

		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"browseDir","requestId":"r1","channelId":"ch1","machineId":"m1","path":"/tmp"}`))
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"readFile","requestId":"r2","channelId":"ch1","machineId":"m1","path":"/tmp/file.txt","maxBytes":100}`))

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

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(dispatched)
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case err := <-errCh:
			t.Fatalf("expected handler error to be isolated, got %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(dispatched) != 2 {
		t.Fatalf("expected 2 dispatched messages despite handler error, got %d", len(dispatched))
	}
}

func TestPingManagerSignalsAfterConsecutiveFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = c.Close(websocket.StatusNormalClosure, "closed for ping test")
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	errCh := make(chan error, 1)
	go pingManager(ctx, conn, 50*time.Millisecond, 2, errCh)

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected ping manager error")
		}
	case <-time.After(12 * time.Second):
		t.Fatal("ping manager did not signal failure")
	}
}
