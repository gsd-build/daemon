package preview

import (
	"context"
	"testing"
	"time"
)

func TestRegistryOpenReplacesSessionPreview(t *testing.T) {
	r := NewRegistry()
	exp := time.Now().Add(time.Hour)
	if err := r.Open(context.Background(), OpenRequest{
		PreviewID: "preview_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		MachineID: "machine_1",
		Target:    Target{Host: "127.0.0.1", Port: 3000},
		ExpiresAt: exp,
	}); err != nil {
		t.Fatalf("open first: %v", err)
	}
	if err := r.Open(context.Background(), OpenRequest{
		PreviewID: "preview_2",
		SessionID: "session_1",
		ChannelID: "channel_1",
		MachineID: "machine_1",
		Target:    Target{Host: "127.0.0.1", Port: 5173},
		ExpiresAt: exp,
	}); err != nil {
		t.Fatalf("open replacement: %v", err)
	}
	if _, ok := r.Get("preview_1"); ok {
		t.Fatal("old preview still active")
	}
	got, ok := r.Get("preview_2")
	if !ok || got.Target.Port != 5173 {
		t.Fatalf("replacement missing or wrong target: %#v", got)
	}
}

func TestRegistryCloseCancelsStreams(t *testing.T) {
	r := NewRegistry()
	if err := r.Open(context.Background(), OpenRequest{
		PreviewID: "preview_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		MachineID: "machine_1",
		Target:    Target{Host: "127.0.0.1", Port: 3000},
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel, ok := r.RegisterStream("preview_1", "stream_1")
	if !ok {
		t.Fatal("stream not registered")
	}
	defer cancel()
	r.Close("preview_1")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream context not canceled")
	}
}

func TestRegistryEnforcesActiveStreamLimit(t *testing.T) {
	r := NewRegistry()
	if err := r.Open(context.Background(), OpenRequest{
		PreviewID: "preview_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		MachineID: "machine_1",
		Target:    Target{Host: "127.0.0.1", Port: 3000},
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	cancels := make([]context.CancelFunc, 0, DefaultMaxActiveStreams)
	for i := 0; i < DefaultMaxActiveStreams; i++ {
		_, cancel, ok := r.RegisterStream("preview_1", string(rune('a'+i)))
		if !ok {
			t.Fatalf("stream %d rejected before active limit", i)
		}
		cancels = append(cancels, cancel)
	}
	for _, cancel := range cancels {
		defer cancel()
	}
	if _, _, ok := r.RegisterStream("preview_1", "overflow"); ok {
		t.Fatal("overflow stream registered")
	}
}
