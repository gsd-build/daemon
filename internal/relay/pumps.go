package relay

import (
	"context"
	"fmt"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

// writePump drains sendCh and writes each message to the WebSocket.
// Exits on ctx cancellation, write error, or when sendCh is closed.
// Sends any error to errCh.
func writePump(ctx context.Context, conn *websocket.Conn, sendCh <-chan []byte, errCh chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case buf, ok := <-sendCh:
			if !ok {
				return // channel closed
			}
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, buf)
			cancel()
			if err != nil {
				errCh <- fmt.Errorf("write pump: %w", err)
				return
			}
		}
	}
}

// readPump reads frames from the WebSocket, parses envelopes, and
// dispatches to the handler. Exits on ctx cancellation or read error.
// Sends any error to errCh.
func readPump(ctx context.Context, conn *websocket.Conn, handler MessageHandler, errCh chan<- error) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // clean shutdown
			}
			errCh <- fmt.Errorf("read pump: %w", err)
			return
		}
		env, err := protocol.ParseEnvelope(data)
		if err != nil {
			continue // skip malformed frames
		}
		if handler != nil {
			if err := handler(env); err != nil {
				errCh <- fmt.Errorf("handler: %w", err)
				return
			}
		}
	}
}
