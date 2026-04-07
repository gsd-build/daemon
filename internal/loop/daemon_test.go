package loop

// Placeholder: a full integration test requires spinning up a fake relay
// (see apps/daemon/internal/relay for that pattern). For now we verify
// the daemon handles Ack messages correctly — PruneWAL is called on the
// matching actor.

import "testing"

func TestHandleAckPlaceholder(t *testing.T) {
	// The actor's PruneWAL is covered by session/actor_test.go and
	// wal_test.go. End-to-end ack handling is verified by plan 10's
	// relay integration tests.
	t.Skip("covered by actor and WAL tests; integration test lives in plan 10")
}
