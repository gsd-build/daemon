package lab

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestLabServerFakeModeExportsSession(t *testing.T) {
	store, err := NewSessionStore(SessionStoreOptions{
		RootDir: t.TempDir(),
		Config:  SessionConfig{Provider: "claude-cli", Model: "claude-sonnet-4-6", FakeMode: true},
	})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	relay := NewLocalRelay(LocalRelayOptions{MachineID: "lab-machine", AuthToken: "lab-token", Store: store})
	server := NewServer(ServerOptions{Store: store, Relay: relay, CWD: t.TempDir()})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	httpServer := &http.Server{Handler: server}
	go func() {
		_ = httpServer.Serve(listener)
	}()
	defer httpServer.Shutdown(context.Background())

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + listener.Addr().String() + "/api/export")
	if err != nil {
		t.Fatalf("get export: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
