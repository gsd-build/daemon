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
	openRequest    OpenRequest
	browserID      string
	owner          ControlOwner
	controlVersion int64
	expiresAt      time.Time
	frameCancel    context.CancelFunc
	lastFrameSeq   int64
}

type Manager struct {
	service       Service
	sender        Sender
	frameInterval time.Duration
	mu            sync.Mutex
	byID          map[string]*sessionState
	byTask        map[string]*sessionState
	bySession     map[string]*sessionState
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
		bySession:     map[string]*sessionState{},
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
		InitialURL: msg.InitialURL,
		BridgeMode: msg.BridgeMode,
		PreviewID:  msg.PreviewID,
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
	if msg.SessionID != "" {
		m.bySession[msg.SessionID] = state
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
		m.mu.Lock()
		m.removeStateLocked(state)
		m.mu.Unlock()
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.service.Close(closeCtx, state.browserID)
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
	if !ok {
		m.mu.Unlock()
		return
	}
	if time.Now().After(state.expiresAt) {
		req := state.openRequest
		m.removeStateLocked(state)
		m.mu.Unlock()

		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.service.Close(closeCtx, state.browserID)
		_ = m.sender.Send(closeCtx, &protocol.BrowserSessionClosed{
			Type:      protocol.MsgTypeBrowserSessionClosed,
			BrowserID: state.browserID,
			SessionID: req.SessionID,
			ChannelID: req.ChannelID,
			Reason:    "expired",
			ClosedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		})
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
		Type:             protocol.MsgTypeBrowserFrame,
		BrowserID:        browserID,
		SessionID:        req.SessionID,
		ChannelID:        req.ChannelID,
		Seq:              frame.Sequence,
		ContentType:      frame.ContentType,
		DataBase64:       frame.DataBase64,
		Width:            frame.Width,
		Height:           frame.Height,
		ViewportWidth:    frame.ViewportWidth,
		ViewportHeight:   frame.ViewportHeight,
		DevicePixelRatio: frame.DevicePixelRatio,
		CapturedAt:       frame.CapturedAt,
	})
	m.mu.Lock()
	if current := m.byID[browserID]; current == state && frame.Sequence > current.lastFrameSeq {
		current.lastFrameSeq = frame.Sequence
	}
	m.mu.Unlock()
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
	return m.grantForStateLocked(m.byTask[taskID])
}

func (m *Manager) GrantForSession(sessionID string) (Grant, bool) {
	if sessionID == "" {
		return Grant{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.grantForStateLocked(m.bySession[sessionID])
}

func (m *Manager) grantForStateLocked(state *sessionState) (Grant, bool) {
	if state == nil || time.Now().After(state.expiresAt) {
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
	owner, ok := parseControlOwner(msg.Owner)
	if !ok {
		return fmt.Errorf("invalid browser control owner: %s", msg.Owner)
	}
	m.mu.Lock()
	state, ok := m.byID[msg.BrowserID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("browser session not found")
	}
	if msg.ControlVersion != 0 && msg.ControlVersion != state.controlVersion {
		m.mu.Unlock()
		return fmt.Errorf("stale browser control version")
	}
	state.owner = owner
	state.controlVersion++
	out := &protocol.BrowserControlClaim{
		Type:           protocol.MsgTypeBrowserControlClaim,
		BrowserID:      msg.BrowserID,
		SessionID:      state.openRequest.SessionID,
		ChannelID:      state.openRequest.ChannelID,
		Owner:          string(owner),
		Reason:         msg.Reason,
		ControlVersion: state.controlVersion,
	}
	m.mu.Unlock()
	return m.sender.Send(ctx, out)
}

func (m *Manager) Release(ctx context.Context, msg *protocol.BrowserControlRelease) error {
	owner, ok := parseControlOwner(msg.Owner)
	if !ok {
		return fmt.Errorf("invalid browser control owner: %s", msg.Owner)
	}
	m.mu.Lock()
	state, ok := m.byID[msg.BrowserID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("browser session not found")
	}
	if msg.ControlVersion != 0 && msg.ControlVersion != state.controlVersion {
		m.mu.Unlock()
		return fmt.Errorf("stale browser control version")
	}
	if owner == state.owner {
		state.owner = OwnerAgent
		state.controlVersion++
	}
	out := &protocol.BrowserControlRelease{
		Type:           protocol.MsgTypeBrowserControlRelease,
		BrowserID:      msg.BrowserID,
		SessionID:      state.openRequest.SessionID,
		ChannelID:      state.openRequest.ChannelID,
		Owner:          string(state.owner),
		Reason:         msg.Reason,
		ControlVersion: state.controlVersion,
	}
	m.mu.Unlock()
	return m.sender.Send(ctx, out)
}

func (m *Manager) UserInput(ctx context.Context, msg *protocol.BrowserUserInput) error {
	m.mu.Lock()
	state, ok := m.byID[msg.BrowserID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("browser session not found")
	}
	if time.Now().After(state.expiresAt) {
		req := state.openRequest
		version := state.controlVersion
		m.mu.Unlock()
		_ = m.sendInputAck(ctx, req, msg, false, protocol.BrowserInputRejectExpiredGrant, version)
		return fmt.Errorf("browser grant expired")
	}
	inputOwner, ownerOK := parseControlOwner(msg.Owner)
	if !ownerOK || inputOwner != OwnerLex || state.owner != OwnerLex {
		req := state.openRequest
		version := state.controlVersion
		m.mu.Unlock()
		_ = m.sendInputAck(ctx, req, msg, false, protocol.BrowserInputRejectOwnerMismatch, version)
		return fmt.Errorf("browser control belongs to %s", state.owner)
	}
	if msg.ControlVersion != 0 && msg.ControlVersion != state.controlVersion {
		req := state.openRequest
		version := state.controlVersion
		m.mu.Unlock()
		_ = m.sendInputAck(ctx, req, msg, false, protocol.BrowserInputRejectStaleFrame, version)
		return fmt.Errorf("stale browser control version")
	}
	if msg.FrameSeq != 0 && state.lastFrameSeq != 0 && msg.FrameSeq < state.lastFrameSeq-2 {
		req := state.openRequest
		version := state.controlVersion
		m.mu.Unlock()
		_ = m.sendInputAck(ctx, req, msg, false, protocol.BrowserInputRejectStaleFrame, version)
		return fmt.Errorf("stale browser frame")
	}
	req := state.openRequest
	version := state.controlVersion
	m.mu.Unlock()
	if err := m.service.UserInput(ctx, msg.BrowserID, msg); err != nil {
		return err
	}
	return m.sendInputAck(ctx, req, msg, true, "", version)
}

func parseControlOwner(owner string) (ControlOwner, bool) {
	switch owner {
	case string(OwnerAgent):
		return OwnerAgent, true
	case string(OwnerLex):
		return OwnerLex, true
	case string(OwnerPaused):
		return OwnerPaused, true
	case string(OwnerApproval):
		return OwnerApproval, true
	default:
		return "", false
	}
}

func (m *Manager) sendInputAck(ctx context.Context, req OpenRequest, msg *protocol.BrowserUserInput, accepted bool, reason string, version int64) error {
	return m.sender.Send(ctx, &protocol.BrowserUserInputAck{
		Type:           protocol.MsgTypeBrowserUserInputAck,
		BrowserID:      msg.BrowserID,
		SessionID:      req.SessionID,
		ChannelID:      req.ChannelID,
		InputID:        msg.InputID,
		Accepted:       accepted,
		Reason:         reason,
		ControlVersion: version,
		AckedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	})
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
	m.removeStateLocked(state)
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

func (m *Manager) removeStateLocked(state *sessionState) {
	delete(m.byID, state.browserID)
	if state.openRequest.TaskID != "" {
		delete(m.byTask, state.openRequest.TaskID)
	}
	if state.openRequest.SessionID != "" && m.bySession[state.openRequest.SessionID] == state {
		delete(m.bySession, state.openRequest.SessionID)
	}
	if state.frameCancel != nil {
		state.frameCancel()
		state.frameCancel = nil
	}
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
