package preview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

func TestWebSocketOpenPassesSubprotocol(t *testing.T) {
	selected := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"vite-hmr"}})
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		selected <- conn.Subprotocol()
	}))
	defer target.Close()

	registry := NewRegistry()
	mustOpenPreview(t, registry, mustPort(t, target.URL))
	sender := &fakeSender{}
	bridge := NewWebSocketBridge(registry, sender)
	err := bridge.Open(context.Background(), &protocol.PreviewWebSocketOpen{
		Type:      protocol.MsgTypePreviewWebSocketOpen,
		StreamID:  "ws_1",
		PreviewID: "preview_1",
		Path:      "/",
		Protocols: []string{"vite-hmr"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	select {
	case got := <-selected:
		if got != "vite-hmr" {
			t.Fatalf("subprotocol = %q, want vite-hmr", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subprotocol was not selected")
	}
}

func TestWebSocketOpenPreservesPreviewHost(t *testing.T) {
	type seenHeaders struct {
		host          string
		forwardedHost string
	}
	seen := make(chan seenHeaders, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- seenHeaders{
			host:          r.Host,
			forwardedHost: r.Header.Get("X-Forwarded-Host"),
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer target.Close()

	registry := NewRegistry()
	mustOpenPreview(t, registry, mustPort(t, target.URL))
	bridge := NewWebSocketBridge(registry, &fakeSender{})
	err := bridge.Open(context.Background(), &protocol.PreviewWebSocketOpen{
		Type:      protocol.MsgTypePreviewWebSocketOpen,
		StreamID:  "ws_1",
		PreviewID: "preview_1",
		Path:      "/_next/webpack-hmr",
		Headers: map[string][]string{
			"Host":             {"preview_1.preview.gsd.build"},
			"X-Forwarded-Host": {"preview_1.preview.gsd.build"},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	select {
	case got := <-seen:
		if got.host != "preview_1.preview.gsd.build" {
			t.Fatalf("Host = %q, want preview host", got.host)
		}
		if got.forwardedHost != "preview_1.preview.gsd.build" {
			t.Fatalf("X-Forwarded-Host = %q, want preview host", got.forwardedHost)
		}
	case <-time.After(time.Second):
		t.Fatal("target did not receive websocket open")
	}
}

func TestWebSocketCloseClosesLocalTarget(t *testing.T) {
	closed := make(chan struct{})
	target := newPreviewEchoWebSocketServer(t, closed)
	defer target.Close()
	registry := NewRegistry()
	mustOpenPreview(t, registry, mustPort(t, target.URL))
	bridge := NewWebSocketBridge(registry, &fakeSender{})
	if err := bridge.Open(context.Background(), previewWSOpen("ws_1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := bridge.Close(context.Background(), &protocol.PreviewWebSocketClose{
		Type:     protocol.MsgTypePreviewWebSocketClose,
		StreamID: "ws_1",
		Code:     1000,
		Reason:   "normal",
	}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("local websocket did not close")
	}
}

func previewWSOpen(streamID string) *protocol.PreviewWebSocketOpen {
	return &protocol.PreviewWebSocketOpen{
		Type:      protocol.MsgTypePreviewWebSocketOpen,
		StreamID:  streamID,
		PreviewID: "preview_1",
		Path:      "/",
	}
}

func newPreviewEchoWebSocketServer(t *testing.T, closed chan<- struct{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer close(closed)
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			messageType, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			_ = conn.Write(r.Context(), messageType, payload)
		}
	}))
}
