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

func TestClientConnectsAndSendsHello(t *testing.T) {
	var mu sync.Mutex
	var received []any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("accept: %v", err)
			return
		}
		defer c.CloseNow()

		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			env, err := protocol.ParseEnvelope(data)
			if err != nil {
				t.Logf("parse: %v", err)
				return
			}
			mu.Lock()
			received = append(received, env.Payload)
			mu.Unlock()

			// Send back a welcome after hello
			if env.Type == protocol.MsgTypeHello {
				welcome := protocol.Welcome{
					Type:                    protocol.MsgTypeWelcome,
					AckedSequencesBySession: map[string]int64{},
				}
				buf, _ := json.Marshal(welcome)
				_ = c.Write(ctx, websocket.MessageText, buf)
				return
			}
		}
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/daemon?token=secret"

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

	welcomeCh := make(chan *protocol.Welcome, 1)
	go func() {
		w, err := client.Connect(ctx)
		if err != nil {
			t.Logf("connect: %v", err)
		}
		welcomeCh <- w
	}()

	select {
	case w := <-welcomeCh:
		if w == nil {
			t.Fatal("nil welcome")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connect did not return welcome")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 {
		t.Fatal("server received nothing")
	}
	hello, ok := received[0].(*protocol.Hello)
	if !ok {
		t.Fatalf("first frame not a Hello: %T", received[0])
	}
	if hello.MachineID != "m-1" {
		t.Errorf("unexpected machine id: %s", hello.MachineID)
	}
}
