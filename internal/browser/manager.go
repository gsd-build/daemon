package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

type Sender interface {
	Send(context.Context, any) error
}

type ManagerOptions struct {
	Service       Service
	Sender        Sender
	FrameInterval time.Duration
}

type sessionState struct {
	openRequest OpenRequest
	browserID   string
	owner       ControlOwner
	expiresAt   time.Time
	frameCancel context.CancelFunc
}

type Manager struct {
	service       Service
	sender        Sender
	frameInterval time.Duration
	mu            sync.Mutex
	byID          map[string]*sessionState
	byTask        map[string]*sessionState
}

func NewManager(opts ManagerOptions) *Manager {
	frameInterval := opts.FrameInterval
	if frameInterval <= 0 {
		frameInterval = 2 * time.Second
	}
	return &Manager{
		service:       opts.Service,
		sender:        opts.Sender,
		frameInterval: frameInterval,
		byID:          map[string]*sessionState{},
		byTask:        map[string]*sessionState{},
	}
}

type Grant struct {
	GrantID   string
	BrowserID string
	SessionID string
	TaskID    string
}

func (m *Manager) Open(ctx context.Context, msg *protocol.BrowserSessionOpen) error {
	expiresAt, err := time.Parse(time.RFC3339Nano, msg.ExpiresAt)
	if err != nil {
		return fmt.Errorf("invalid browser grant expiry")
	}
	req := OpenRequest{
		GrantID:    msg.GrantID,
		SessionID:  msg.SessionID,
		ProjectID:  msg.ProjectID,
		TaskID:     msg.TaskID,
		ChannelID:  msg.ChannelID,
		MachineID:  msg.MachineID,
		IdentityID: msg.IdentityID,
		Mode:       msg.Mode,
		ExpiresAt:  msg.ExpiresAt,
	}
	result, err := m.service.Open(ctx, req)
	if err != nil {
		return err
	}
	frameCtx, frameCancel := context.WithCancel(context.Background())
	m.mu.Lock()
	state := &sessionState{
		openRequest: req,
		browserID:   result.BrowserID,
		owner:       OwnerAgent,
		expiresAt:   expiresAt,
		frameCancel: frameCancel,
	}
	m.byID[result.BrowserID] = state
	if msg.TaskID != "" {
		m.byTask[msg.TaskID] = state
	}
	m.mu.Unlock()
	if err := m.sender.Send(ctx, &protocol.BrowserSessionOpened{
		Type:      protocol.MsgTypeBrowserSessionOpened,
		RequestID: msg.RequestID,
		BrowserID: result.BrowserID,
		GrantID:   msg.GrantID,
		SessionID: msg.SessionID,
		ChannelID: msg.ChannelID,
		URL:       result.URL,
		Title:     result.Title,
		OpenedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		frameCancel()
		return err
	}
	go m.frameLoop(frameCtx, result.BrowserID)
	return nil
}

func (m *Manager) frameLoop(ctx context.Context, browserID string) {
	m.sendFrame(ctx, browserID)
	ticker := time.NewTicker(m.frameInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sendFrame(ctx, browserID)
		}
	}
}

func (m *Manager) sendFrame(ctx context.Context, browserID string) {
	m.mu.Lock()
	state, ok := m.byID[browserID]
	if !ok || time.Now().After(state.expiresAt) {
		m.mu.Unlock()
		return
	}
	req := state.openRequest
	m.mu.Unlock()

	frameCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	frame, err := m.service.Frame(frameCtx, browserID)
	if err != nil {
		return
	}
	_ = m.sender.Send(ctx, &protocol.BrowserFrame{
		Type:        protocol.MsgTypeBrowserFrame,
		BrowserID:   browserID,
		SessionID:   req.SessionID,
		ChannelID:   req.ChannelID,
		Seq:         frame.Sequence,
		ContentType: frame.ContentType,
		DataBase64:  frame.DataBase64,
		Width:       frame.Width,
		Height:      frame.Height,
		CapturedAt:  frame.CapturedAt,
	})
	if frame.URL != "" {
		_ = m.sender.Send(ctx, &protocol.BrowserNavigation{
			Type:      protocol.MsgTypeBrowserNavigation,
			BrowserID: browserID,
			SessionID: req.SessionID,
			ChannelID: req.ChannelID,
			URL:       frame.URL,
			Title:     frame.Title,
			EndedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
}

func (m *Manager) GrantForTask(taskID string) (Grant, bool) {
	if taskID == "" {
		return Grant{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.byTask[taskID]
	if !ok || time.Now().After(state.expiresAt) {
		return Grant{}, false
	}
	return Grant{
		GrantID:   state.openRequest.GrantID,
		BrowserID: state.browserID,
		SessionID: state.openRequest.SessionID,
		TaskID:    state.openRequest.TaskID,
	}, true
}

func (m *Manager) Claim(ctx context.Context, msg *protocol.BrowserControlClaim) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.byID[msg.BrowserID]
	if !ok {
		return fmt.Errorf("browser session not found")
	}
	state.owner = ControlOwner(msg.Owner)
	return nil
}

func (m *Manager) Release(ctx context.Context, msg *protocol.BrowserControlRelease) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.byID[msg.BrowserID]
	if !ok {
		return fmt.Errorf("browser session not found")
	}
	if ControlOwner(msg.Owner) == state.owner {
		state.owner = OwnerAgent
	}
	return nil
}

func (m *Manager) UserInput(ctx context.Context, msg *protocol.BrowserUserInput) error {
	m.mu.Lock()
	state, ok := m.byID[msg.BrowserID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("browser session not found")
	}
	if state.owner != OwnerLex {
		m.mu.Unlock()
		return fmt.Errorf("browser control belongs to %s", state.owner)
	}
	m.mu.Unlock()
	return m.service.UserInput(ctx, msg.BrowserID, msg)
}

func (m *Manager) Close(ctx context.Context, msg *protocol.BrowserSessionClose) error {
	m.mu.Lock()
	state, ok := m.byID[msg.BrowserID]
	if !ok {
		for _, candidate := range m.byID {
			if candidate.openRequest.GrantID == msg.GrantID {
				state = candidate
				ok = true
				break
			}
		}
	}
	if !ok {
		m.mu.Unlock()
		return nil
	}
	delete(m.byID, state.browserID)
	if state.openRequest.TaskID != "" {
		delete(m.byTask, state.openRequest.TaskID)
	}
	if state.frameCancel != nil {
		state.frameCancel()
	}
	m.mu.Unlock()

	_ = m.service.Close(ctx, state.browserID)
	return m.sender.Send(ctx, &protocol.BrowserSessionClosed{
		Type:      protocol.MsgTypeBrowserSessionClosed,
		BrowserID: state.browserID,
		SessionID: state.openRequest.SessionID,
		ChannelID: state.openRequest.ChannelID,
		Reason:    msg.Reason,
		ClosedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (m *Manager) Tool(ctx context.Context, msg *protocol.BrowserToolCall) error {
	m.mu.Lock()
	state, ok := m.byID[msg.BrowserID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("browser session not found")
	}
	if time.Now().After(state.expiresAt) {
		m.mu.Unlock()
		return fmt.Errorf("browser grant expired")
	}
	if state.owner != OwnerAgent {
		m.mu.Unlock()
		return fmt.Errorf("browser control belongs to %s", state.owner)
	}
	m.mu.Unlock()
	result, err := m.service.Tool(ctx, msg.BrowserID, msg.Method, msg.ParamsJSON)
	if err != nil {
		return err
	}
	return m.sender.Send(ctx, &protocol.BrowserToolResult{
		Type:       protocol.MsgTypeBrowserToolResult,
		BrowserID:  msg.BrowserID,
		GrantID:    msg.GrantID,
		TaskID:     msg.TaskID,
		ToolUseID:  msg.ToolUseID,
		OK:         result.OK,
		ResultJSON: result.ResultJSON,
		Error:      result.Error,
	})
}
