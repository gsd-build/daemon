package lab

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	protocol "github.com/gsd-build/protocol-go"
)

type LocalRelayOptions struct {
	MachineID string
	AuthToken string
	Store     *SessionStore
}

type LocalRelay struct {
	machineID string
	authToken string
	store     *SessionStore
	mu        sync.Mutex
	daemon    *websocket.Conn
	ui        map[*websocket.Conn]struct{}
}

func NewLocalRelay(opts LocalRelayOptions) *LocalRelay {
	return &LocalRelay{
		machineID: opts.MachineID,
		authToken: opts.AuthToken,
		store:     opts.Store,
		ui:        make(map[*websocket.Conn]struct{}),
	}
}

func (r *LocalRelay) HandleDaemonWS(w http.ResponseWriter, req *http.Request) {
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer "+r.authToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	r.mu.Lock()
	r.daemon = conn
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.daemon == conn {
			r.daemon = nil
		}
		r.mu.Unlock()
	}()

	ctx := req.Context()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		_ = r.store.Append("relay.daemon.recv", json.RawMessage(append([]byte(nil), data...)))
		env, err := protocol.ParseEnvelope(data)
		if err == nil && env.Type == protocol.MsgTypeHello {
			_ = r.SendToDaemon(ctx, &protocol.Welcome{Type: protocol.MsgTypeWelcome})
		}
		r.broadcastUI(ctx, data)
	}
}

func (r *LocalRelay) HandleUIWS(w http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	r.mu.Lock()
	r.ui[conn] = struct{}{}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.ui, conn)
		r.mu.Unlock()
	}()

	ctx := req.Context()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		_ = r.store.Append("relay.ui.recv", json.RawMessage(append([]byte(nil), data...)))
		if env, err := protocol.ParseEnvelope(data); err == nil {
			_ = r.store.Append("ui."+env.Type, json.RawMessage(append([]byte(nil), data...)))
		}
		_ = r.SendRawToDaemon(ctx, data)
	}
}

func (r *LocalRelay) SendToDaemon(ctx context.Context, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return r.SendRawToDaemon(ctx, data)
}

func (r *LocalRelay) SendRawToDaemon(ctx context.Context, data []byte) error {
	r.mu.Lock()
	conn := r.daemon
	r.mu.Unlock()
	if conn == nil {
		return nil
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		return err
	}
	_ = r.store.Append("relay.daemon.send", json.RawMessage(append([]byte(nil), data...)))
	return nil
}

func (r *LocalRelay) broadcastUI(ctx context.Context, data []byte) {
	r.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(r.ui))
	for conn := range r.ui {
		conns = append(conns, conn)
	}
	r.mu.Unlock()
	for _, conn := range conns {
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = conn.Write(writeCtx, websocket.MessageText, data)
		cancel()
	}
}
