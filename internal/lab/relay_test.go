package lab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

func TestLocalRelayWelcomesDaemonAndRecordsFrames(t *testing.T) {
	store, err := NewSessionStore(SessionStoreOptions{RootDir: t.TempDir(), Config: SessionConfig{}})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	relay := NewLocalRelay(LocalRelayOptions{
		MachineID: "machine-lab",
		AuthToken: "token-lab",
		Store:     store,
	})
	server := httptest.NewServer(http.HandlerFunc(relay.HandleDaemonWS))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer token-lab"}},
	})
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer conn.CloseNow()

	hello := `{"type":"hello","machineId":"machine-lab","daemonVersion":"test","os":"darwin","arch":"arm64"}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(hello)); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	env, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatalf("parse welcome: %v", err)
	}
	if env.Type != protocol.MsgTypeWelcome {
		t.Fatalf("type = %q, want welcome", env.Type)
	}

	bundle, err := store.Export(ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(bundle.Events) == 0 {
		t.Fatal("expected recorded relay event")
	}
}

func TestLocalRelayForwardsUITaskToDaemon(t *testing.T) {
	store, err := NewSessionStore(SessionStoreOptions{RootDir: t.TempDir(), Config: SessionConfig{}})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	relay := NewLocalRelay(LocalRelayOptions{MachineID: "machine-lab", AuthToken: "token-lab", Store: store})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/daemon":
			relay.HandleDaemonWS(w, r)
		case "/ui":
			relay.HandleUIWS(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	daemon, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):]+"/daemon", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer token-lab"}},
	})
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	defer daemon.CloseNow()
	ui, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):]+"/ui", nil)
	if err != nil {
		t.Fatalf("dial ui: %v", err)
	}
	defer ui.CloseNow()

	if err := ui.Write(ctx, websocket.MessageText, []byte(`{"type":"task","taskId":"t1","sessionId":"s1","channelId":"c1","prompt":"hi","engine":"pi","provider":"claude-cli","model":"claude-sonnet-4-6","cwd":"/tmp"}`)); err != nil {
		t.Fatalf("write ui task: %v", err)
	}
	_, data, err := daemon.Read(ctx)
	if err != nil {
		t.Fatalf("daemon read: %v", err)
	}
	env, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	if env.Type != protocol.MsgTypeTask {
		t.Fatalf("type = %q, want task", env.Type)
	}
}
