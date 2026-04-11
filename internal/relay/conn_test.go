package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
