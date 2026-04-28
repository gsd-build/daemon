package preview

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

func TestHandleHTTPRequestRewritesPreviewHostToLocalTarget(t *testing.T) {
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
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	port := mustPort(t, target.URL)
	registry := NewRegistry()
	mustOpenPreview(t, registry, port)
	sender := &fakeSender{}
	handler := &HTTPHandler{Registry: registry, Sender: sender, Client: target.Client()}

	err := handler.Handle(context.Background(), &protocol.PreviewHTTPRequest{
		Type:      protocol.MsgTypePreviewHTTPRequest,
		RequestID: "req_1",
		StreamID:  "stream_1",
		PreviewID: "preview_1",
		Method:    http.MethodGet,
		Path:      "/",
		Headers:   map[string][]string{"host": {"preview_1.preview.gsd.build"}},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := <-seen
	wantHost := mustURLHost(t, target.URL)
	if got.host != wantHost {
		t.Fatalf("Host = %q, want local target %q", got.host, wantHost)
	}
	if got.forwardedHost != "preview_1.preview.gsd.build" {
		t.Fatalf("X-Forwarded-Host = %q, want preview host", got.forwardedHost)
	}
}

func TestHandleHTTPRequestFallsBackToIPv6Loopback(t *testing.T) {
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	target.Listener = listener
	target.Start()
	defer target.Close()

	registry := NewRegistry()
	mustOpenPreview(t, registry, mustPort(t, target.URL))
	sender := &fakeSender{}
	handler := &HTTPHandler{Registry: registry, Sender: sender, Client: target.Client()}

	if err := handler.Handle(context.Background(), previewGET("stream_1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	chunks := messagesOfType[*protocol.PreviewStreamChunk](sender)
	if len(chunks) == 0 {
		t.Fatal("no response chunks sent")
	}
}

func TestHandleHTTPRequestStreamsLargeResponse(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 2*1024*1024)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer target.Close()

	registry := NewRegistry()
	mustOpenPreview(t, registry, mustPort(t, target.URL))
	sender := &fakeSender{}
	handler := &HTTPHandler{Registry: registry, Sender: sender, Client: target.Client()}

	if err := handler.Handle(context.Background(), previewGET("stream_1")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	chunks := messagesOfType[*protocol.PreviewStreamChunk](sender)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want multiple chunks", len(chunks))
	}
	if !chunks[len(chunks)-1].Final {
		t.Fatal("last chunk Final=false, want true")
	}
}

func TestHandleHTTPRequestRejectsUnknownPreview(t *testing.T) {
	sender := &fakeSender{}
	handler := &HTTPHandler{Registry: NewRegistry(), Sender: sender, Client: http.DefaultClient}
	if err := handler.Handle(context.Background(), previewGET("stream_1")); err == nil {
		t.Fatal("Handle succeeded for unknown preview")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent %d messages, want 0", len(sender.sent))
	}
}

type fakeSender struct {
	mu   sync.Mutex
	sent []any
}

func (f *fakeSender) Send(_ context.Context, msg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return nil
}

func messagesOfType[T any](f *fakeSender) []T {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []T
	for _, msg := range f.sent {
		if typed, ok := msg.(T); ok {
			out = append(out, typed)
		}
	}
	return out
}

func mustPort(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return port
}

func mustURLHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return u.Host
}

func mustOpenPreview(t *testing.T, registry *Registry, port int) {
	t.Helper()
	if err := registry.Open(context.Background(), OpenRequest{
		PreviewID: "preview_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		MachineID: "machine_1",
		Target:    Target{Host: "127.0.0.1", Port: port},
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("open preview: %v", err)
	}
}

func previewGET(streamID string) *protocol.PreviewHTTPRequest {
	return &protocol.PreviewHTTPRequest{
		Type:      protocol.MsgTypePreviewHTTPRequest,
		RequestID: "req_1",
		StreamID:  streamID,
		PreviewID: "preview_1",
		Method:    http.MethodGet,
		Path:      "/",
		Headers:   map[string][]string{"host": {"preview_1.preview.gsd.build"}},
	}
}
