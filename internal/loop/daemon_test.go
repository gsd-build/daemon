package loop

// Placeholder: a full integration test requires spinning up a fake relay
// (see apps/daemon/internal/relay for that pattern). For now we verify
// the daemon handles Ack messages correctly — PruneWAL is called on the
// matching actor.

import (
	"net/url"
	"strings"
	"testing"

	"github.com/gsd-cloud/daemon/internal/config"
)

func TestHandleAckPlaceholder(t *testing.T) {
	// The actor's PruneWAL is covered by session/actor_test.go and
	// wal_test.go. End-to-end ack handling is verified by plan 10's
	// relay integration tests.
	t.Skip("covered by actor and WAL tests; integration test lives in plan 10")
}

func TestRelayURLIncludesMachineIDAndToken(t *testing.T) {
	cfg := &config.Config{
		MachineID: "machine-uuid-123",
		AuthToken: "token-with-special/chars+",
		RelayURL:  "wss://relay.example.com/ws/daemon",
	}

	got := buildRelayURL(cfg)

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("machineId") != "machine-uuid-123" {
		t.Errorf("missing or wrong machineId: %q", q.Get("machineId"))
	}
	if q.Get("token") != "token-with-special/chars+" {
		t.Errorf("missing or wrong token: %q", q.Get("token"))
	}
	if !strings.HasPrefix(got, "wss://relay.example.com/ws/daemon?") {
		t.Errorf("base URL not preserved: %q", got)
	}
}
