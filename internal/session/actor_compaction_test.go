package session

import (
	"context"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/pi"
	protocol "github.com/gsd-build/protocol-go"
)

func int64Ptr(v int64) *int64     { return &v }
func floatPtr(v float64) *float64 { return &v }

func TestActorHandlesContextStatsRequest(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	relay := newFakeRelay()
	actor := &Actor{
		opts: Options{
			SessionID: "session_123",
			CWD:       t.TempDir(),
			Relay:     relay,
		},
	}
	actor.now = func() time.Time { return now }
	actor.runPiControl = func(ctx context.Context, command pi.ControlCommand, onEvent func(pi.ControlEvent)) (pi.ControlResult, error) {
		if command.Type != pi.ControlCommandGetSessionStats {
			t.Fatalf("command type = %q", command.Type)
		}
		return pi.ControlResult{
			OK: true,
			ContextUsage: &pi.ContextUsage{
				Tokens:        int64Ptr(270000),
				ContextWindow: 1000000,
				Percent:       floatPtr(27),
			},
		}, nil
	}

	actor.handleContextStatsRequest(context.Background(), &protocol.ContextStatsRequest{
		Type:      protocol.MsgTypeContextStatsRequest,
		SessionID: "session_123",
		ChannelID: "channel_123",
		RequestID: "stats_123",
	})

	frames := relay.GetFrames()
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}
	message := frames[0].(*protocol.ContextStats)
	if message.Tokens == nil || *message.Tokens != 270000 {
		t.Fatalf("tokens = %+v", message.Tokens)
	}
	if message.AutoThresholdPercent != 98.3616 {
		t.Fatalf("auto threshold = %f", message.AutoThresholdPercent)
	}
	if message.ObservedAt != now {
		t.Fatalf("observed at = %s", message.ObservedAt)
	}
}

func TestActorHandlesCompactRequestLifecycle(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	relay := newFakeRelay()
	actor := &Actor{
		opts: Options{
			SessionID: "session_123",
			CWD:       t.TempDir(),
			Relay:     relay,
		},
	}
	actor.now = func() time.Time { return now }
	actor.runPiControl = func(ctx context.Context, command pi.ControlCommand, onEvent func(pi.ControlEvent)) (pi.ControlResult, error) {
		if command.Type != pi.ControlCommandCompact {
			t.Fatalf("command type = %q", command.Type)
		}
		if command.CustomInstructions != "preserve auth state" {
			t.Fatalf("instructions = %q", command.CustomInstructions)
		}
		onEvent(pi.ControlEvent{
			Type: pi.ControlEventCompactionStart,
			ContextUsage: &pi.ContextUsage{
				Tokens:        int64Ptr(8951),
				ContextWindow: 1000000,
				Percent:       floatPtr(0.8951),
			},
		})
		onEvent(pi.ControlEvent{
			Type:             pi.ControlEventCompactionEnd,
			Summary:          "Kept auth state.",
			FirstKeptEntryID: "entry_42",
			ContextUsage: &pi.ContextUsage{
				Tokens:        int64Ptr(7712),
				ContextWindow: 1000000,
				Percent:       floatPtr(0.7712),
			},
		})
		return pi.ControlResult{OK: true}, nil
	}

	actor.handleCompactRequest(context.Background(), &protocol.CompactRequest{
		Type:         protocol.MsgTypeCompactRequest,
		SessionID:    "session_123",
		ChannelID:    "channel_123",
		RequestID:    "compact_123",
		Instructions: "preserve auth state",
	})

	frames := relay.GetFrames()
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}
	started := frames[0].(*protocol.CompactStatus)
	if started.Status != protocol.CompactStatusStarted {
		t.Fatalf("started status = %q", started.Status)
	}
	if started.TokensBefore == nil || *started.TokensBefore != 8951 {
		t.Fatalf("tokens before = %+v", started.TokensBefore)
	}

	completed := frames[1].(*protocol.CompactStatus)
	if completed.Status != protocol.CompactStatusCompleted {
		t.Fatalf("completed status = %q", completed.Status)
	}
	if completed.Summary != "Kept auth state." {
		t.Fatalf("summary = %q", completed.Summary)
	}
	if completed.TokensAfter == nil || *completed.TokensAfter != 7712 {
		t.Fatalf("tokens after = %+v", completed.TokensAfter)
	}
}
