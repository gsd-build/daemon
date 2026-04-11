package relay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSendEnqueuesWhenCapacityAvailable(t *testing.T) {
	ch := make(chan []byte, 512)
	s := &sender{sendCh: ch}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	msg := map[string]string{"type": "test"}
	if err := s.Send(ctx, msg); err != nil {
		t.Fatalf("send should succeed: %v", err)
	}

	select {
	case buf := <-ch:
		var got map[string]string
		if err := json.Unmarshal(buf, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["type"] != "test" {
			t.Errorf("expected type=test, got %s", got["type"])
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestSendTimesOutWhenChannelFull(t *testing.T) {
	ch := make(chan []byte, 1)
	s := &sender{sendCh: ch}

	// Fill the channel
	ch <- []byte("blocked")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := s.Send(ctx, map[string]string{"type": "overflow"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected deadline exceeded, got: %v", err)
	}
}

func TestSendReturnsErrorOnCancelledContext(t *testing.T) {
	ch := make(chan []byte, 1)
	ch <- []byte("blocked")
	s := &sender{sendCh: ch}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := s.Send(ctx, map[string]string{"type": "cancelled"})
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context canceled, got: %v", err)
	}
}
