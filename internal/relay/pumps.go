package relay

import (
	"context"
	"fmt"
	"time"

	"github.com/coder/websocket"
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
