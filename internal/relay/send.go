package relay

import (
	"context"
	"encoding/json"
	"fmt"

	protocol "github.com/gsd-build/protocol-go"
)

// sender holds the send channel. Embedded in Client.
type sender struct {
	sendCh chan []byte
}

// Send marshals msg to JSON and enqueues it onto the send channel.
// Blocks until the message is enqueued or ctx expires.
func (s *sender) Send(ctx context.Context, msg any) error {
	buf, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	select {
	case s.sendCh <- buf:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("send: %w", ctx.Err())
	}
}

func (s *sender) drainQueued(ctx context.Context) (*protocol.Envelope, error) {
	select {
	case buf := <-s.sendCh:
		env, err := protocol.ParseEnvelope(buf)
		if err != nil {
			return nil, fmt.Errorf("parse queued envelope: %w", err)
		}
		return env, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
