package relay

import (
	"context"
	"testing"
	"time"
)

func TestWakeDetectorReturnsChannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	ch := WakeDetector(ctx)
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	// We can't easily simulate sleep in a test, but we can verify
	// the detector runs without blocking or panicking.
	<-ctx.Done()
}
