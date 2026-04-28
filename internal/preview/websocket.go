package preview

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

type WebSocketBridge struct {
	Registry *Registry
	Sender   Sender

	mu      sync.Mutex
	streams map[string]*wsStream
}

type wsStream struct {
	previewID string
	conn      *websocket.Conn
	cancel    context.CancelFunc
	writeCh   chan *protocol.PreviewWebSocketData
}

func NewWebSocketBridge(registry *Registry, sender Sender) *WebSocketBridge {
	return &WebSocketBridge{
		Registry: registry,
		Sender:   sender,
		streams:  map[string]*wsStream{},
	}
}

func (b *WebSocketBridge) Open(ctx context.Context, msg *protocol.PreviewWebSocketOpen) error {
	preview, ok := b.Registry.Get(msg.PreviewID)
	if !ok {
		return fmt.Errorf("preview not active")
	}
	streamCtx, cancel, ok := b.Registry.RegisterStream(msg.PreviewID, msg.StreamID)
	if !ok {
		return fmt.Errorf("stream not registered")
	}
	mergedCtx, mergedCancel := mergeStreamContext(ctx, streamCtx, cancel)
	closeStream := func() {
		mergedCancel()
		cancel()
	}

	if msg.Path == "" || !strings.HasPrefix(msg.Path, "/") || strings.HasPrefix(msg.Path, "//") {
		closeStream()
		b.Registry.UnregisterStream(msg.PreviewID, msg.StreamID)
		return fmt.Errorf("preview path must be origin-form")
	}

	header := http.Header{}
	copyRequestHeaders(header, msg.Headers)
	conn, err := b.openTargetWebSocket(mergedCtx, preview.Target, msg, header)
	if err != nil {
		closeStream()
		b.Registry.UnregisterStream(msg.PreviewID, msg.StreamID)
		return err
	}
	conn.SetReadLimit(DefaultMaxFrameBytes)

	b.mu.Lock()
	stream := &wsStream{previewID: msg.PreviewID, conn: conn, cancel: closeStream, writeCh: make(chan *protocol.PreviewWebSocketData, 64)}
	b.streams[msg.StreamID] = stream
	b.mu.Unlock()

	if err := b.Sender.Send(ctx, &protocol.PreviewWebSocketOpenResult{
		Type:      protocol.MsgTypePreviewWebSocketOpenResult,
		StreamID:  msg.StreamID,
		PreviewID: msg.PreviewID,
		OK:        true,
		Protocol:  conn.Subprotocol(),
	}); err != nil {
		_ = b.Close(ctx, &protocol.PreviewWebSocketClose{StreamID: msg.StreamID, Code: int(websocket.StatusInternalError), Reason: "send_open_result_failed"})
		return err
	}

	go b.readLoop(mergedCtx, msg.StreamID, msg.PreviewID, conn)
	go b.writeLoop(mergedCtx, msg.StreamID, stream)
	return nil
}

func (b *WebSocketBridge) openTargetWebSocket(ctx context.Context, target Target, msg *protocol.PreviewWebSocketOpen, baseHeader http.Header) (*websocket.Conn, error) {
	var lastErr error
	for _, addr := range target.Addrs() {
		header := baseHeader.Clone()
		if previewHost := firstHeader(msg.Headers, "host"); previewHost != "" {
			header.Set("X-Forwarded-Host", previewHost)
		}
		localURL := "ws://" + addr + msg.Path
		conn, resp, err := websocket.Dial(ctx, localURL, &websocket.DialOptions{
			HTTPHeader:   header,
			Host:         addr,
			Subprotocols: msg.Protocols,
		})
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (b *WebSocketBridge) Data(ctx context.Context, msg *protocol.PreviewWebSocketData) error {
	stream, ok := b.get(msg.StreamID)
	if !ok {
		return fmt.Errorf("websocket stream not active")
	}
	select {
	case stream.writeCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("websocket stream write queue full")
	}
}

func (b *WebSocketBridge) writeLoop(ctx context.Context, streamID string, stream *wsStream) {
	for {
		select {
		case msg := <-stream.writeCh:
			if err := b.writeData(ctx, stream, msg); err != nil {
				_ = b.Close(context.Background(), &protocol.PreviewWebSocketClose{
					Type:     protocol.MsgTypePreviewWebSocketClose,
					StreamID: streamID,
					Code:     int(websocket.StatusPolicyViolation),
					Reason:   "write_failed",
				})
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (b *WebSocketBridge) writeData(ctx context.Context, stream *wsStream, msg *protocol.PreviewWebSocketData) error {
	payload, err := base64.StdEncoding.DecodeString(msg.BodyBase64)
	if err != nil {
		return fmt.Errorf("decode websocket payload: %w", err)
	}
	if len(payload) > DefaultMaxFrameBytes {
		return fmt.Errorf("websocket payload exceeds frame limit")
	}
	messageType := websocket.MessageText
	if msg.IsBinary {
		messageType = websocket.MessageBinary
	}
	return stream.conn.Write(ctx, messageType, payload)
}

func (b *WebSocketBridge) Close(ctx context.Context, msg *protocol.PreviewWebSocketClose) error {
	stream, ok := b.remove(msg.StreamID)
	if !ok {
		return nil
	}
	code := websocket.StatusNormalClosure
	if msg.Code != 0 {
		code = websocket.StatusCode(msg.Code)
	}
	stream.cancel()
	b.Registry.UnregisterStream(stream.previewID, msg.StreamID)
	return stream.conn.Close(code, msg.Reason)
}

func (b *WebSocketBridge) readLoop(ctx context.Context, streamID, previewID string, conn *websocket.Conn) {
	var sequence int64
	defer func() {
		conn.CloseNow()
		b.remove(streamID)
		b.Registry.UnregisterStream(previewID, streamID)
	}()
	for {
		messageType, payload, err := conn.Read(ctx)
		if err != nil {
			if err := b.Sender.Send(ctx, &protocol.PreviewWebSocketClose{
				Type:     protocol.MsgTypePreviewWebSocketClose,
				StreamID: streamID,
				Code:     int(websocket.CloseStatus(err)),
				Reason:   err.Error(),
			}); err != nil {
				return
			}
			return
		}
		sequence++
		if err := b.Sender.Send(ctx, &protocol.PreviewWebSocketData{
			Type:       protocol.MsgTypePreviewWebSocketData,
			StreamID:   streamID,
			Sequence:   sequence,
			IsBinary:   messageType == websocket.MessageBinary,
			BodyBase64: base64.StdEncoding.EncodeToString(payload),
		}); err != nil {
			return
		}
	}
}

func mergeStreamContext(parent context.Context, stream context.Context, streamCancel context.CancelFunc) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-stream.Done():
			cancel()
		case <-ctx.Done():
			streamCancel()
		}
	}()
	return ctx, func() {
		cancel()
		streamCancel()
	}
}

func (b *WebSocketBridge) get(streamID string) (*wsStream, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stream, ok := b.streams[streamID]
	return stream, ok
}

func (b *WebSocketBridge) remove(streamID string) (*wsStream, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stream, ok := b.streams[streamID]
	if ok {
		delete(b.streams, streamID)
	}
	return stream, ok
}
