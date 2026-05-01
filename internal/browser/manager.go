package browser

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	openRequest         OpenRequest
	browserID           string
	owner               ControlOwner
	controlVersion      int64
	expiresAt           time.Time
	frameCancel         context.CancelFunc
	lastFrameSeq        int64
	lastFrameCapturedAt time.Time
}

type Manager struct {
	service          Service
	sender           Sender
	frameInterval    time.Duration
	mu               sync.Mutex
	byID             map[string]*sessionState
	byTask           map[string]*sessionState
	bySession        map[string]*sessionState
	pendingApprovals map[string]*pendingApproval
}

func NewManager(opts ManagerOptions) *Manager {
	frameInterval := opts.FrameInterval
	if frameInterval <= 0 {
		frameInterval = 2 * time.Second
	}
	return &Manager{
		service:          opts.Service,
		sender:           opts.Sender,
		frameInterval:    frameInterval,
		byID:             map[string]*sessionState{},
		byTask:           map[string]*sessionState{},
		bySession:        map[string]*sessionState{},
		pendingApprovals: map[string]*pendingApproval{},
	}
}

type pendingApproval struct {
	ApprovalID             string
	Nonce                  string
	ExpiresAt              time.Time
	ActorUserID            string
	GrantID                string
	BrowserID              string
	ToolUseID              string
	Method                 string
	Category               string
	Origin                 string
	ParameterHash          string
	ExpectedExternalEffect string
	Sensitivity            string
	Consumed               bool
	PriorOwner             ControlOwner
	Request                OpenRequest
	ToolCall               protocol.BrowserToolCall
}

type Grant struct {
	GrantID   string
	BrowserID string
	SessionID string
	ChannelID string
	TaskID    string
}

func (m *Manager) Ensure(ctx context.Context, req EnsureRequest) (Grant, error) {
	if grant, ok := m.GrantForTask(req.TaskID); ok {
		return grant, nil
	}
	if grant, ok := m.GrantForSession(req.SessionID); ok {
		return grant, nil
	}
	if req.GrantID == "" {
		return Grant{}, fmt.Errorf("browser grant missing")
	}
	if req.SessionID == "" {
		return Grant{}, fmt.Errorf("browser session missing")
	}
	if req.ExpiresAt == "" {
		return Grant{}, fmt.Errorf("browser grant expiry missing")
	}
	if err := m.Open(ctx, &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: fmt.Sprintf("browser_lazy_%d", time.Now().UnixNano()),
		GrantID:   req.GrantID,
		SessionID: req.SessionID,
		ProjectID: req.ProjectID,
		TaskID:    req.TaskID,
		ChannelID: req.ChannelID,
		MachineID: req.MachineID,
		Mode:      "clean",
		ExpiresAt: req.ExpiresAt,
	}); err != nil {
		return Grant{}, err
	}
	if grant, ok := m.GrantForTask(req.TaskID); ok {
		return grant, nil
	}
	if grant, ok := m.GrantForSession(req.SessionID); ok {
		return grant, nil
	}
	return Grant{}, fmt.Errorf("browser grant unavailable after open")
}

func (m *Manager) Open(ctx context.Context, msg *protocol.BrowserSessionOpen) error {
	expiresAt, err := time.Parse(time.RFC3339Nano, msg.ExpiresAt)
	if err != nil {
		return fmt.Errorf("invalid browser grant expiry")
	}
	req := OpenRequest{
		GrantID:           msg.GrantID,
		SessionID:         msg.SessionID,
		ProjectID:         msg.ProjectID,
		TaskID:            msg.TaskID,
		ChannelID:         msg.ChannelID,
		MachineID:         msg.MachineID,
		IdentityID:        msg.IdentityID,
		IdentityScope:     msg.IdentityScope,
		IdentityKey:       msg.IdentityKey,
		IdentityProjectID: msg.IdentityProjectID,
		IdentitySessionID: msg.IdentitySessionID,
		Mode:              msg.Mode,
		InitialURL:        msg.InitialURL,
		BridgeMode:        msg.BridgeMode,
		PreviewID:         msg.PreviewID,
		ExpiresAt:         msg.ExpiresAt,
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
		Type:                   protocol.MsgTypeBrowserFrame,
		BrowserID:              browserID,
		SessionID:              req.SessionID,
		ChannelID:              req.ChannelID,
		Seq:                    frame.Sequence,
		ContentType:            frame.ContentType,
		DataBase64:             frame.DataBase64,
		Width:                  frame.Width,
		Height:                 frame.Height,
		ViewportWidth:          frame.ViewportWidth,
		ViewportHeight:         frame.ViewportHeight,
		ViewportCSSWidth:       frame.ViewportCSSWidth,
		ViewportCSSHeight:      frame.ViewportCSSHeight,
		CapturePixelWidth:      frame.CapturePixelWidth,
		CapturePixelHeight:     frame.CapturePixelHeight,
		DevicePixelRatio:       frame.DevicePixelRatio,
		CaptureScaleX:          frame.CaptureScaleX,
		CaptureScaleY:          frame.CaptureScaleY,
		EncodedBytes:           frame.EncodedBytes,
		Quality:                frame.Quality,
		CapturePixelRatio:      frame.CapturePixelRatio,
		LatencyMS:              frame.LatencyMS,
		LatestAcceptedFrameSeq: frame.LatestAcceptedFrameSeq,
		CapturedAt:             frame.CapturedAt,
	})
	m.sendRefs(ctx, browserID)
	m.mu.Lock()
	if current := m.byID[browserID]; current == state && frame.Sequence > current.lastFrameSeq {
		current.lastFrameSeq = frame.Sequence
		current.lastFrameCapturedAt = time.Now()
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

func (m *Manager) sendRefs(ctx context.Context, browserID string) {
	m.mu.Lock()
	state, ok := m.byID[browserID]
	if !ok {
		m.mu.Unlock()
		return
	}
	req := state.openRequest
	m.mu.Unlock()

	refsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	refs, err := m.service.Refs(refsCtx, browserID)
	if err != nil {
		return
	}

	out := make([]protocol.BrowserRef, 0, len(refs.Refs))
	for _, ref := range refs.Refs {
		out = append(out, protocol.BrowserRef{
			Ref:  ref.Ref,
			Key:  ref.Key,
			Role: ref.Role,
			Name: ref.Name,
			X:    ref.X,
			Y:    ref.Y,
			W:    ref.W,
			H:    ref.H,
		})
	}

	capturedAt := refs.CapturedAt
	if capturedAt == "" {
		capturedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_ = m.sender.Send(ctx, &protocol.BrowserRefs{
		Type:       protocol.MsgTypeBrowserRefs,
		BrowserID:  browserID,
		SessionID:  req.SessionID,
		ChannelID:  req.ChannelID,
		Version:    refs.Version,
		Refs:       out,
		CapturedAt: capturedAt,
	})
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
		ChannelID: state.openRequest.ChannelID,
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
	req := state.openRequest
	version := state.controlVersion
	out := &protocol.BrowserControlClaim{
		Type:           protocol.MsgTypeBrowserControlClaim,
		BrowserID:      msg.BrowserID,
		SessionID:      req.SessionID,
		ChannelID:      req.ChannelID,
		Owner:          string(owner),
		Reason:         msg.Reason,
		ControlVersion: version,
	}
	m.mu.Unlock()
	if err := m.sender.Send(ctx, out); err != nil {
		return err
	}
	return m.sendControlState(ctx, msg.BrowserID, req, owner, version, true, msg.Reason)
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
	req := state.openRequest
	currentOwner := state.owner
	version := state.controlVersion
	out := &protocol.BrowserControlRelease{
		Type:           protocol.MsgTypeBrowserControlRelease,
		BrowserID:      msg.BrowserID,
		SessionID:      req.SessionID,
		ChannelID:      req.ChannelID,
		Owner:          string(currentOwner),
		Reason:         msg.Reason,
		ControlVersion: version,
	}
	m.mu.Unlock()
	if err := m.sender.Send(ctx, out); err != nil {
		return err
	}
	return m.sendControlState(ctx, msg.BrowserID, req, currentOwner, version, true, msg.Reason)
}

func (m *Manager) sendControlState(ctx context.Context, browserID string, req OpenRequest, owner ControlOwner, version int64, accepted bool, reason string) error {
	return m.sender.Send(ctx, &protocol.BrowserControlState{
		Type:           protocol.MsgTypeBrowserControlState,
		BrowserID:      browserID,
		SessionID:      req.SessionID,
		ChannelID:      req.ChannelID,
		Owner:          string(owner),
		ControlVersion: version,
		Accepted:       accepted,
		Reason:         reason,
		At:             time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (m *Manager) ClaimAndInput(ctx context.Context, msg *protocol.BrowserClaimAndInput) error {
	if err := m.Claim(ctx, &protocol.BrowserControlClaim{
		Type:      protocol.MsgTypeBrowserControlClaim,
		BrowserID: msg.BrowserID,
		SessionID: msg.SessionID,
		ChannelID: msg.ChannelID,
		Owner:     protocol.BrowserOwnerLex,
		Reason:    msg.ClaimID,
	}); err != nil {
		return err
	}
	input := msg.Input
	input.BrowserID = msg.BrowserID
	input.SessionID = msg.SessionID
	input.ChannelID = msg.ChannelID
	input.Owner = protocol.BrowserOwnerLex
	m.mu.Lock()
	if state, ok := m.byID[msg.BrowserID]; ok {
		input.ControlVersion = state.controlVersion
	}
	m.mu.Unlock()
	return m.UserInput(ctx, &input)
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
		_ = m.sendInputAck(ctx, req, msg, false, protocol.BrowserInputRejectStaleControlVersion, version)
		return fmt.Errorf("stale browser control version")
	}
	if requiresViewportCSS(msg) && msg.CoordinateSpace != protocol.BrowserInputCoordinateViewportCSS {
		req := state.openRequest
		version := state.controlVersion
		m.mu.Unlock()
		_ = m.sendInputAck(ctx, req, msg, false, protocol.BrowserInputRejectInvalidPayload, version)
		return fmt.Errorf("browser input requires viewport_css coordinate space")
	}
	if msg.FrameSeq != 0 && state.lastFrameSeq != 0 && msg.FrameSeq < state.lastFrameSeq-2 {
		req := state.openRequest
		version := state.controlVersion
		m.mu.Unlock()
		_ = m.sendInputAck(ctx, req, msg, false, protocol.BrowserInputRejectStaleFrame, version)
		return fmt.Errorf("stale browser frame")
	}
	if staleByAge(state, msg) {
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
		Type:             protocol.MsgTypeBrowserUserInputAck,
		BrowserID:        msg.BrowserID,
		SessionID:        req.SessionID,
		ChannelID:        req.ChannelID,
		InputID:          msg.InputID,
		Kind:             msg.Kind,
		Phase:            msg.Phase,
		Accepted:         accepted,
		FrameSeq:         msg.FrameSeq,
		AcceptedFrameSeq: msg.FrameSeq,
		ReasonCode:       reason,
		SafeRetry:        reason == protocol.BrowserInputRejectStaleFrame,
		Message:          browserInputAckMessage(accepted, reason),
		Reason:           reason,
		ControlVersion:   version,
		AckedAt:          time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func browserInputAckMessage(accepted bool, reason string) string {
	if accepted {
		return "input accepted"
	}
	switch reason {
	case protocol.BrowserInputRejectStaleControlVersion:
		return "Control version stale, refresh pending"
	case protocol.BrowserInputRejectStaleFrame:
		return "Frame stale, refresh pending"
	case protocol.BrowserInputRejectOwnerMismatch:
		return "Browser control owner mismatch"
	case protocol.BrowserInputRejectInvalidPayload:
		return "Invalid browser input payload"
	case protocol.BrowserInputRejectExpiredGrant:
		return "Browser grant expired"
	default:
		return "Browser input rejected"
	}
}

func (m *Manager) setLastFrameForTest(browserID string, seq int64, capturedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state := m.byID[browserID]; state != nil {
		state.lastFrameSeq = seq
		state.lastFrameCapturedAt = capturedAt
	}
}

func requiresViewportCSS(msg *protocol.BrowserUserInput) bool {
	return msg.Kind == protocol.BrowserInputKindPointer || msg.Kind == protocol.BrowserInputKindWheel
}

func staleByAge(state *sessionState, msg *protocol.BrowserUserInput) bool {
	if state.lastFrameCapturedAt.IsZero() {
		return false
	}
	age := time.Since(state.lastFrameCapturedAt)
	switch msg.Kind {
	case protocol.BrowserInputKindPointer:
		if msg.Phase == "click" || msg.Phase == "down" {
			return age > 250*time.Millisecond
		}
	case protocol.BrowserInputKindWheel, protocol.BrowserInputKindKey:
		return age > 500*time.Millisecond
	}
	return false
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
	_, err := m.ToolResult(ctx, msg)
	return err
}

func (m *Manager) ToolResult(ctx context.Context, msg *protocol.BrowserToolCall) (ToolResult, error) {
	m.mu.Lock()
	state, ok := m.byID[msg.BrowserID]
	if !ok {
		m.mu.Unlock()
		return ToolResult{}, fmt.Errorf("browser session not found")
	}
	if time.Now().After(state.expiresAt) {
		m.mu.Unlock()
		return ToolResult{}, fmt.Errorf("browser grant expired")
	}
	if state.owner != OwnerAgent {
		req := state.openRequest
		risk := classifyBrowserTool(msg.Method, msg.ParamsJSON)
		if risk == BrowserRiskModelVisibleCapture {
			result := ToolResult{
				OK:        false,
				Error:     "model-visible capture blocked while Lex controls browser",
				ErrorCode: "model_visible_capture_blocked",
			}
			m.mu.Unlock()
			_ = m.sendToolResult(ctx, msg, req, result)
			return result, fmt.Errorf("model-visible capture blocked while %s controls browser", state.owner)
		}
		m.mu.Unlock()
		return ToolResult{}, fmt.Errorf("browser control belongs to %s", state.owner)
	}
	req := state.openRequest
	risk := classifyBrowserTool(msg.Method, msg.ParamsJSON)
	summary := browserToolSummary(msg.Method, risk)
	if err := validateBrowserToolPolicy(msg.Method, msg.ParamsJSON); err != nil {
		m.mu.Unlock()
		result := ToolResult{OK: false, Error: err.Error(), ErrorCode: "browser_policy_blocked"}
		_ = m.sendToolStarted(ctx, msg, req, risk, summary)
		_ = m.sendToolUpdated(ctx, msg, req, "blocked", summary, nil)
		_ = m.sendToolResult(ctx, msg, req, result)
		return result, err
	}
	if isCredentialBrowserTool(msg.Method, risk) {
		m.mu.Unlock()
		_ = m.sendToolStarted(ctx, msg, req, risk, summary)
		_ = m.sendToolUpdated(ctx, msg, req, "rejected", summary, nil)
		result := ToolResult{
			OK:        false,
			Error:     "browser credential methods are not available to agents",
			ErrorCode: "feature_not_enabled",
		}
		if err := m.sendToolResult(ctx, msg, req, result); err != nil {
			return result, fmt.Errorf("send browser credential method rejection: %w", err)
		}
		return result, fmt.Errorf("browser credential methods are not available to agents")
	}
	if browserRiskRequiresApproval(risk) {
		previousOwner := state.owner
		previousVersion := state.controlVersion
		state.owner = OwnerApproval
		state.controlVersion++
		nextVersion := state.controlVersion
		approvalID := fmt.Sprintf("approval_%d", time.Now().UnixNano())
		nonce := fmt.Sprintf("nonce_%d", time.Now().UnixNano())
		expiresAt := time.Now().Add(2 * time.Minute)
		parameterHash := browserParameterHash(msg.ParamsJSON)
		m.pendingApprovals[approvalID] = &pendingApproval{
			ApprovalID:             approvalID,
			Nonce:                  nonce,
			ExpiresAt:              expiresAt,
			ActorUserID:            "lex",
			GrantID:                msg.GrantID,
			BrowserID:              msg.BrowserID,
			ToolUseID:              msg.ToolUseID,
			Method:                 msg.Method,
			Category:               string(risk),
			Origin:                 browserOrigin(msg.ParamsJSON),
			ParameterHash:          parameterHash,
			ExpectedExternalEffect: browserApprovalSummary(msg.Method, risk),
			Sensitivity:            string(risk),
			PriorOwner:             previousOwner,
			Request:                req,
			ToolCall:               *msg,
		}
		m.mu.Unlock()
		_ = m.sendToolStarted(ctx, msg, req, risk, summary)
		_ = m.sendToolUpdated(ctx, msg, req, "approval_required", summary, nil)
		requestID := fmt.Sprintf("browser_sensitive_%d", time.Now().UnixNano())
		if err := m.sender.Send(ctx, &protocol.BrowserSensitiveActionRequest{
			Type:                   protocol.MsgTypeBrowserSensitiveActionRequest,
			BrowserID:              msg.BrowserID,
			RequestID:              requestID,
			SessionID:              req.SessionID,
			ChannelID:              req.ChannelID,
			TaskID:                 msg.TaskID,
			ApprovalID:             approvalID,
			Nonce:                  nonce,
			ActorUserID:            "lex",
			GrantID:                msg.GrantID,
			ToolUseID:              msg.ToolUseID,
			Method:                 msg.Method,
			Category:               string(risk),
			Summary:                browserApprovalSummary(msg.Method, risk),
			Origin:                 browserOrigin(msg.ParamsJSON),
			ParameterHash:          parameterHash,
			ExpectedExternalEffect: browserApprovalSummary(msg.Method, risk),
			Sensitivity:            string(risk),
			ExpiresAt:              expiresAt.UTC().Format(time.RFC3339Nano),
		}); err != nil {
			m.mu.Lock()
			delete(m.pendingApprovals, approvalID)
			if current := m.byID[msg.BrowserID]; current == state &&
				current.owner == OwnerApproval &&
				current.controlVersion == nextVersion {
				current.owner = previousOwner
				current.controlVersion = previousVersion
			}
			m.mu.Unlock()
			return ToolResult{}, fmt.Errorf("send browser sensitive action request: %w", err)
		}
		return ToolResult{OK: false, Error: "browser action requires approval", ErrorCode: "approval_required"}, fmt.Errorf("browser action requires approval: %s", risk)
	}
	m.mu.Unlock()
	if err := m.sendToolStarted(ctx, msg, req, risk, summary); err != nil {
		return ToolResult{}, err
	}
	result, err := m.service.Tool(ctx, msg.BrowserID, msg.Method, msg.ParamsJSON)
	if err != nil {
		result = ToolResult{OK: false, Error: err.Error(), ErrorCode: "browser_tool_failed"}
		_ = m.sendToolUpdated(ctx, msg, req, "error", summary, nil)
		_ = m.sendToolResult(ctx, msg, req, result)
		return result, err
	}
	status := "ok"
	if !result.OK {
		status = "error"
	}
	if err := m.sendToolUpdated(ctx, msg, req, status, summary, result.ResultJSON); err != nil {
		return result, err
	}
	return result, m.sendToolResult(ctx, msg, req, result)
}

func (m *Manager) SensitiveActionResponse(ctx context.Context, msg *protocol.BrowserSensitiveActionResponse) error {
	m.mu.Lock()
	pending := m.pendingApprovals[msg.ApprovalID]
	if pending == nil {
		m.mu.Unlock()
		return fmt.Errorf("browser approval not found")
	}
	if pending.Consumed {
		m.mu.Unlock()
		return fmt.Errorf("browser approval already consumed")
	}
	if time.Now().After(pending.ExpiresAt) {
		pending.Consumed = true
		m.restoreApprovalOwnerLocked(pending)
		m.mu.Unlock()
		return fmt.Errorf("browser approval expired")
	}
	if err := validateApprovalResponse(pending, msg); err != nil {
		m.mu.Unlock()
		return err
	}
	pending.Consumed = true
	m.restoreApprovalOwnerLocked(pending)
	toolCall := pending.ToolCall
	req := pending.Request
	m.mu.Unlock()

	if !msg.Approved {
		_ = m.sendBrowserEvidence(ctx, req, toolCall.BrowserID, "denied", "approval", msg.DeniedReason)
		return fmt.Errorf("browser action denied")
	}

	result, err := m.service.Tool(ctx, toolCall.BrowserID, toolCall.Method, toolCall.ParamsJSON)
	if err != nil {
		result = ToolResult{OK: false, Error: err.Error(), ErrorCode: "browser_tool_failed"}
		_ = m.sendToolUpdated(ctx, &toolCall, req, "error", browserToolSummary(toolCall.Method, BrowserRiskExternalEffect), nil)
		_ = m.sendToolResult(ctx, &toolCall, req, result)
		return err
	}
	_ = m.sendToolUpdated(ctx, &toolCall, req, "ok", browserToolSummary(toolCall.Method, classifyBrowserTool(toolCall.Method, toolCall.ParamsJSON)), result.ResultJSON)
	return m.sendToolResult(ctx, &toolCall, req, result)
}

func (m *Manager) restoreApprovalOwnerLocked(pending *pendingApproval) {
	if state := m.byID[pending.BrowserID]; state != nil && state.owner == OwnerApproval {
		state.owner = pending.PriorOwner
		state.controlVersion++
	}
}

func validateApprovalResponse(pending *pendingApproval, msg *protocol.BrowserSensitiveActionResponse) error {
	checks := map[string][2]string{
		"nonce":                  {pending.Nonce, msg.Nonce},
		"actorUserId":            {pending.ActorUserID, msg.ActorUserID},
		"grantId":                {pending.GrantID, msg.GrantID},
		"browserId":              {pending.BrowserID, msg.BrowserID},
		"toolUseId":              {pending.ToolUseID, msg.ToolUseID},
		"method":                 {pending.Method, msg.Method},
		"category":               {pending.Category, msg.Category},
		"origin":                 {pending.Origin, msg.Origin},
		"parameterHash":          {pending.ParameterHash, msg.ParameterHash},
		"expectedExternalEffect": {pending.ExpectedExternalEffect, msg.ExpectedExternalEffect},
		"sensitivity":            {pending.Sensitivity, msg.Sensitivity},
	}
	for field, pair := range checks {
		if pair[0] != pair[1] {
			return fmt.Errorf("browser approval %s mismatch", field)
		}
	}
	return nil
}

func browserParameterHash(params json.RawMessage) string {
	sum := sha256.Sum256(params)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func browserOrigin(params json.RawMessage) string {
	var payload struct {
		Origin string `json:"origin"`
		URL    string `json:"url"`
	}
	_ = json.Unmarshal(params, &payload)
	if payload.Origin != "" {
		return payload.Origin
	}
	return payload.URL
}

func (m *Manager) sendBrowserEvidence(ctx context.Context, req OpenRequest, browserID string, status string, eventType string, summary string) error {
	return m.sender.Send(ctx, &protocol.BrowserEvidenceCreated{
		Type:            protocol.MsgTypeBrowserEvidenceCreated,
		EvidenceID:      fmt.Sprintf("evidence_%d", time.Now().UnixNano()),
		BrowserID:       browserID,
		SessionID:       req.SessionID,
		ChannelID:       req.ChannelID,
		Actor:           "daemon",
		Status:          status,
		EventType:       eventType,
		Summary:         summary,
		RedactionStatus: "safe",
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (m *Manager) sendToolStarted(ctx context.Context, msg *protocol.BrowserToolCall, req OpenRequest, risk BrowserRisk, summary string) error {
	return m.sender.Send(ctx, &protocol.BrowserToolCallStarted{
		Type:      protocol.MsgTypeBrowserToolCallStarted,
		BrowserID: msg.BrowserID,
		GrantID:   msg.GrantID,
		SessionID: req.SessionID,
		ChannelID: req.ChannelID,
		TaskID:    msg.TaskID,
		ToolUseID: msg.ToolUseID,
		Method:    msg.Method,
		Category:  string(risk),
		Summary:   summary,
		Metadata:  safeToolMetadata(msg.Method, msg.ParamsJSON),
		At:        time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (m *Manager) sendToolUpdated(ctx context.Context, msg *protocol.BrowserToolCall, req OpenRequest, status string, summary string, metadata json.RawMessage) error {
	return m.sender.Send(ctx, &protocol.BrowserToolCallUpdated{
		Type:      protocol.MsgTypeBrowserToolCallUpdated,
		BrowserID: msg.BrowserID,
		GrantID:   msg.GrantID,
		SessionID: req.SessionID,
		ChannelID: req.ChannelID,
		TaskID:    msg.TaskID,
		ToolUseID: msg.ToolUseID,
		Status:    status,
		Summary:   summary,
		Metadata:  metadata,
		At:        time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (m *Manager) sendToolResult(ctx context.Context, msg *protocol.BrowserToolCall, req OpenRequest, result ToolResult) error {
	return m.sender.Send(ctx, &protocol.BrowserToolResult{
		Type:            protocol.MsgTypeBrowserToolResult,
		BrowserID:       msg.BrowserID,
		GrantID:         msg.GrantID,
		SessionID:       req.SessionID,
		ChannelID:       req.ChannelID,
		TaskID:          msg.TaskID,
		ToolUseID:       msg.ToolUseID,
		OK:              result.OK,
		ResultJSON:      result.ResultJSON,
		Error:           result.Error,
		ErrorCode:       result.ErrorCode,
		Sensitivity:     "public",
		RedactionStatus: "not_needed",
	})
}

func browserApprovalSummary(method string, risk BrowserRisk) string {
	return fmt.Sprintf("Run browser method %s (%s)", method, risk)
}

func browserToolSummary(method string, risk BrowserRisk) string {
	return fmt.Sprintf("Run browser method %s (%s)", method, risk)
}

func isCredentialBrowserTool(method string, risk BrowserRisk) bool {
	if risk == BrowserRiskCredentialAuth {
		return true
	}
	switch method {
	case "save_state", "restore_state", "vault_save", "vault_login", "vault_list":
		return true
	default:
		return false
	}
}

func safeToolMetadata(method string, params json.RawMessage) json.RawMessage {
	if method != "navigate" || len(params) == 0 {
		return nil
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(params, &payload); err != nil || payload.URL == "" {
		return nil
	}
	data, err := json.Marshal(map[string]string{"url": payload.URL})
	if err != nil {
		return nil
	}
	return data
}
