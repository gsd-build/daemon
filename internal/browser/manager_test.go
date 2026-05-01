package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

type fakeService struct {
	mu        sync.Mutex
	calls     []string
	refs      []Ref
	toolCalls int
}

func (f *fakeService) Open(ctx context.Context, req OpenRequest) (OpenResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "open:"+req.GrantID)
	return OpenResult{BrowserID: "browser_1", URL: "about:blank", Title: "Blank"}, nil
}

func (f *fakeService) Close(ctx context.Context, browserID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "close:"+browserID)
	return nil
}

func (f *fakeService) Frame(ctx context.Context, browserID string) (Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "frame:"+browserID)
	return Frame{
		Sequence:         1,
		ContentType:      "image/jpeg",
		DataBase64:       "aGVsbG8=",
		Width:            1280,
		Height:           720,
		ViewportWidth:    1280,
		ViewportHeight:   720,
		DevicePixelRatio: 2,
		CapturedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func (f *fakeService) Refs(ctx context.Context, browserID string) (Refs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "refs:"+browserID)
	return Refs{
		Version:    1,
		Refs:       append([]Ref(nil), f.refs...),
		CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func (f *fakeService) Tool(ctx context.Context, browserID string, method string, params []byte) (ToolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "tool:"+method)
	f.toolCalls++
	return ToolResult{OK: true, ResultJSON: []byte(`{"ok":true}`)}, nil
}

func (r *recordingSender) hasType(messageType protocol.MessageType) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, msg := range r.msgs {
		switch messageType {
		case protocol.MsgTypeBrowserRefs:
			if _, ok := msg.(*protocol.BrowserRefs); ok {
				return true
			}
		case protocol.MsgTypeBrowserFrame:
			if _, ok := msg.(*protocol.BrowserFrame); ok {
				return true
			}
		case protocol.MsgTypeBrowserToolResult:
			if _, ok := msg.(*protocol.BrowserToolResult); ok {
				return true
			}
		case protocol.MsgTypeBrowserSensitiveActionRequest:
			if _, ok := msg.(*protocol.BrowserSensitiveActionRequest); ok {
				return true
			}
		}
	}
	return false
}

func (f *fakeService) UserInput(ctx context.Context, browserID string, input *protocol.BrowserUserInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "input:"+input.Kind)
	return nil
}

func (f *fakeService) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type recordingSender struct {
	mu   sync.Mutex
	msgs []any
}

func (r *recordingSender) Send(ctx context.Context, msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, msg)
	return nil
}

func (r *recordingSender) snapshot() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]any(nil), r.msgs...)
}

type failingSender struct{}

func (failingSender) Send(ctx context.Context, msg any) error {
	return errors.New("send failed")
}

func TestBrowserUserInputParamsPreservesZeroRenderedOrigin(t *testing.T) {
	params := browserUserInputParams(&protocol.BrowserUserInput{
		Type:            protocol.MsgTypeBrowserUserInput,
		Kind:            protocol.BrowserInputKindClick,
		CoordinateSpace: protocol.BrowserCoordinateSpaceFrameCssPixels,
		RenderedLeft:    0,
		RenderedTop:     0,
		RenderedWidth:   800,
		RenderedHeight:  600,
	})

	if value, ok := params["renderedLeft"]; !ok || value != float64(0) {
		t.Fatalf("renderedLeft = %#v, %v; want 0 and present", value, ok)
	}
	if value, ok := params["renderedTop"]; !ok || value != float64(0) {
		t.Fatalf("renderedTop = %#v, %v; want 0 and present", value, ok)
	}
}

func TestBrowserUserInputToolRoutesSidebarCommands(t *testing.T) {
	method, params, ok := browserUserInputTool(&protocol.BrowserUserInput{
		Type: protocol.MsgTypeBrowserUserInput,
		Kind: protocol.BrowserInputKindNavigate,
		Text: "https://example.com",
	})
	if !ok {
		t.Fatal("expected navigate input to route through browser tool")
	}
	if method != "navigate" {
		t.Fatalf("method = %q, want navigate", method)
	}
	var payload map[string]string
	if err := json.Unmarshal(params, &payload); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if payload["url"] != "https://example.com" {
		t.Fatalf("url = %q", payload["url"])
	}

	method, params, ok = browserUserInputTool(&protocol.BrowserUserInput{
		Type: protocol.MsgTypeBrowserUserInput,
		Kind: protocol.BrowserInputKindRefAction,
		Text: "@v1:button-primary",
	})
	if !ok {
		t.Fatal("expected ref action input to route through browser tool")
	}
	if method != "click_ref" {
		t.Fatalf("method = %q, want click_ref", method)
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		t.Fatalf("unmarshal ref params: %v", err)
	}
	if payload["ref"] != "@v1:button-primary" {
		t.Fatalf("ref = %q", payload["ref"])
	}
}

func openBrowserForTest(t *testing.T, m *Manager, browserID string) {
	t.Helper()
	if err := m.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_1",
		SessionID: "session_1",
		ProjectID: "project_1",
		TaskID:    "task_1",
		ChannelID: "channel_1",
		MachineID: "machine_1",
		Mode:      "clean",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("open browser: %v", err)
	}
	if browserID != "" {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.byID[browserID]; !ok {
			t.Fatalf("browser %s not opened", browserID)
		}
	}
}

func TestManagerForwardsBrowserRefs(t *testing.T) {
	service := &fakeService{
		refs: []Ref{{
			Ref:  "@v1:e1",
			Key:  "e1",
			Role: "button",
			Name: "Submit",
			X:    10,
			Y:    20,
			W:    80,
			H:    32,
		}},
	}
	sender := &recordingSender{}
	m := NewManager(ManagerOptions{
		Service:       service,
		Sender:        sender,
		FrameInterval: time.Hour,
	})

	err := m.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("open browser: %v", err)
	}

	m.sendRefs(context.Background(), "browser_1")

	if !sender.hasType(protocol.MsgTypeBrowserRefs) {
		t.Fatalf("expected browserRefs message, got %#v", sender.snapshot())
	}
}

func TestManagerBlocksSensitiveToolUntilApproval(t *testing.T) {
	service := &fakeService{}
	sender := &recordingSender{}
	m := NewManager(ManagerOptions{Service: service, Sender: sender, FrameInterval: time.Hour})
	openBrowserForTest(t, m, "browser_1")

	err := m.Tool(context.Background(), &protocol.BrowserToolCall{
		Type:      protocol.MsgTypeBrowserToolCall,
		BrowserID: "browser_1",
		GrantID:   "grant_1",
		TaskID:    "task_1",
		ToolUseID: "tool_1",
		Method:    "fill_form",
	})

	if err == nil {
		t.Fatal("expected sensitive tool to wait for approval")
	}
	service.mu.Lock()
	toolCalls := service.toolCalls
	service.mu.Unlock()
	if toolCalls != 0 {
		t.Fatalf("sensitive tool executed before approval")
	}
	if !sender.hasType(protocol.MsgTypeBrowserSensitiveActionRequest) {
		t.Fatalf("expected sensitive action request, got %#v", sender.snapshot())
	}
}

func TestManagerSendsToolLifecycleEventsAndResultContext(t *testing.T) {
	service := &fakeService{}
	sender := &recordingSender{}
	m := NewManager(ManagerOptions{Service: service, Sender: sender, FrameInterval: time.Hour})
	openBrowserForTest(t, m, "browser_1")

	err := m.Tool(context.Background(), &protocol.BrowserToolCall{
		Type:       protocol.MsgTypeBrowserToolCall,
		BrowserID:  "browser_1",
		GrantID:    "grant_1",
		TaskID:     "task_1",
		ToolUseID:  "tool_1",
		Method:     "navigate",
		ParamsJSON: json.RawMessage(`{"url":"https://example.com"}`),
	})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}

	var started *protocol.BrowserToolCallStarted
	var updated *protocol.BrowserToolCallUpdated
	var result *protocol.BrowserToolResult
	for _, msg := range sender.snapshot() {
		switch typed := msg.(type) {
		case *protocol.BrowserToolCallStarted:
			started = typed
		case *protocol.BrowserToolCallUpdated:
			updated = typed
		case *protocol.BrowserToolResult:
			result = typed
		}
	}
	if started == nil || started.SessionID != "session_1" || started.ChannelID != "channel_1" || started.Category != string(BrowserRiskInspection) {
		t.Fatalf("started = %+v", started)
	}
	if updated == nil || updated.Status != "ok" || updated.SessionID != "session_1" || updated.ChannelID != "channel_1" {
		t.Fatalf("updated = %+v", updated)
	}
	if result == nil || !result.OK || result.SessionID != "session_1" || result.ChannelID != "channel_1" || result.RedactionStatus != "not_needed" {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagerRejectsCredentialMethodsWithoutApproval(t *testing.T) {
	service := &fakeService{}
	sender := &recordingSender{}
	m := NewManager(ManagerOptions{Service: service, Sender: sender, FrameInterval: time.Hour})
	openBrowserForTest(t, m, "browser_1")

	err := m.Tool(context.Background(), &protocol.BrowserToolCall{
		Type:      protocol.MsgTypeBrowserToolCall,
		BrowserID: "browser_1",
		GrantID:   "grant_1",
		TaskID:    "task_1",
		ToolUseID: "tool_1",
		Method:    "vault_login",
	})
	if err == nil {
		t.Fatal("expected credential method rejection")
	}
	if sender.hasType(protocol.MsgTypeBrowserSensitiveActionRequest) {
		t.Fatalf("credential method should not request approval")
	}
	var result *protocol.BrowserToolResult
	for _, msg := range sender.snapshot() {
		if typed, ok := msg.(*protocol.BrowserToolResult); ok {
			result = typed
		}
	}
	if result == nil || result.OK || result.ErrorCode != "feature_not_enabled" {
		t.Fatalf("result = %+v", result)
	}
	service.mu.Lock()
	toolCalls := service.toolCalls
	service.mu.Unlock()
	if toolCalls != 0 {
		t.Fatalf("credential method reached service")
	}
}

func TestManagerRollsBackApprovalOwnerWhenRequestSendFails(t *testing.T) {
	service := &fakeService{}
	m := NewManager(ManagerOptions{Service: service, Sender: &recordingSender{}, FrameInterval: time.Hour})
	openBrowserForTest(t, m, "browser_1")
	m.sender = failingSender{}

	err := m.Tool(context.Background(), &protocol.BrowserToolCall{
		Type:      protocol.MsgTypeBrowserToolCall,
		BrowserID: "browser_1",
		GrantID:   "grant_1",
		TaskID:    "task_1",
		ToolUseID: "tool_1",
		Method:    "fill_form",
	})

	if err == nil {
		t.Fatal("expected send failure")
	}
	m.mu.Lock()
	owner := m.byID["browser_1"].owner
	version := m.byID["browser_1"].controlVersion
	m.mu.Unlock()
	if owner != OwnerAgent {
		t.Fatalf("owner = %s, want %s", owner, OwnerAgent)
	}
	if version != 0 {
		t.Fatalf("controlVersion = %d, want 0", version)
	}
}

func TestClassifyBrowserBatchUsesHighestRiskNestedMethod(t *testing.T) {
	category := classifyBrowserTool("batch", json.RawMessage(`{"steps":[{"action":"mock_route"}]}`))
	if category != BrowserRiskNetworkMutation {
		t.Fatalf("category = %s, want %s", category, BrowserRiskNetworkMutation)
	}
}

func TestClassifyBrowserBatchFailsClosed(t *testing.T) {
	for _, params := range []json.RawMessage{
		json.RawMessage(`not-json`),
		json.RawMessage(`{"steps":[{}]}`),
	} {
		category := classifyBrowserTool("batch", params)
		if category != BrowserRiskExternalEffect {
			t.Fatalf("category = %s, want %s for %s", category, BrowserRiskExternalEffect, params)
		}
	}
}

func TestManagerPausesToolCallsWhileLexControlsBrowser(t *testing.T) {
	svc := &fakeService{}
	sent := &recordingSender{}
	m := NewManager(ManagerOptions{Service: svc, Sender: sent, FrameInterval: time.Hour})

	err := m.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_1",
		SessionID: "session_1",
		ProjectID: "project_1",
		TaskID:    "task_1",
		ChannelID: "channel_1",
		MachineID: "machine_1",
		Mode:      "clean",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if err := m.Claim(context.Background(), &protocol.BrowserControlClaim{
		Type:      protocol.MsgTypeBrowserControlClaim,
		BrowserID: "browser_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		Owner:     "lex",
		Reason:    "pointer",
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	err = m.Tool(context.Background(), &protocol.BrowserToolCall{
		Type:      protocol.MsgTypeBrowserToolCall,
		BrowserID: "browser_1",
		GrantID:   "grant_1",
		TaskID:    "task_1",
		ToolUseID: "toolu_1",
		Method:    "navigate",
	})
	if err == nil {
		t.Fatal("expected tool call to wait or fail while Lex controls browser")
	}
}

func TestManagerTearsDownExpiredSessionOnFrame(t *testing.T) {
	svc := &fakeService{}
	sent := &recordingSender{}
	m := NewManager(ManagerOptions{Service: svc, Sender: sent, FrameInterval: time.Hour})

	if err := m.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_1",
		SessionID: "session_1",
		TaskID:    "task_1",
		ChannelID: "channel_1",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("open: %v", err)
	}

	m.mu.Lock()
	m.byID["browser_1"].expiresAt = time.Now().Add(-time.Second)
	m.mu.Unlock()

	m.sendFrame(context.Background(), "browser_1")

	m.mu.Lock()
	_, hasBrowser := m.byID["browser_1"]
	_, hasTask := m.byTask["task_1"]
	m.mu.Unlock()
	if hasBrowser || hasTask {
		t.Fatal("expected expired browser session to be removed")
	}
	calls := svc.snapshot()
	if !hasCall(calls, "close:browser_1") {
		t.Fatalf("expected browser close call, got %v", calls)
	}
}

func TestManagerIndexesTasklessGrantBySession(t *testing.T) {
	svc := &fakeService{}
	sent := &recordingSender{}
	m := NewManager(ManagerOptions{Service: svc, Sender: sent, FrameInterval: time.Hour})

	if err := m.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("open: %v", err)
	}

	if _, ok := m.GrantForTask("task_1"); ok {
		t.Fatal("taskless grant should not be indexed by task")
	}
	grant, ok := m.GrantForSession("session_1")
	if !ok {
		t.Fatal("expected session grant")
	}
	if grant.GrantID != "grant_1" || grant.BrowserID != "browser_1" || grant.SessionID != "session_1" {
		t.Fatalf("grant = %+v", grant)
	}

	if err := m.Close(context.Background(), &protocol.BrowserSessionClose{
		Type:      protocol.MsgTypeBrowserSessionClose,
		GrantID:   "grant_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		Reason:    "test",
	}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, ok := m.GrantForSession("session_1"); ok {
		t.Fatal("expected session grant to be removed after close")
	}
}

func TestManagerRollsBackIndexedStateWhenOpenSendFails(t *testing.T) {
	svc := &fakeService{}
	m := NewManager(ManagerOptions{Service: svc, Sender: failingSender{}, FrameInterval: time.Hour})

	err := m.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_1",
		SessionID: "session_1",
		TaskID:    "task_1",
		ChannelID: "channel_1",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err == nil {
		t.Fatal("expected open to fail")
	}
	if _, ok := m.GrantForTask("task_1"); ok {
		t.Fatal("expected task grant rollback")
	}
	if _, ok := m.GrantForSession("session_1"); ok {
		t.Fatal("expected session grant rollback")
	}
	calls := svc.snapshot()
	if !hasCall(calls, "close:browser_1") {
		t.Fatalf("expected browser close call, got %v", calls)
	}
}

func TestManagerRejectsExpiredUserInput(t *testing.T) {
	svc := &fakeService{}
	sent := &recordingSender{}
	m := NewManager(ManagerOptions{Service: svc, Sender: sent, FrameInterval: time.Hour})

	if err := m.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_1",
		SessionID: "session_1",
		TaskID:    "task_1",
		ChannelID: "channel_1",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := m.Claim(context.Background(), &protocol.BrowserControlClaim{
		Type:      protocol.MsgTypeBrowserControlClaim,
		BrowserID: "browser_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		Owner:     "lex",
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	m.mu.Lock()
	m.byID["browser_1"].expiresAt = time.Now().Add(-time.Second)
	m.mu.Unlock()

	err := m.UserInput(context.Background(), &protocol.BrowserUserInput{
		Type:      protocol.MsgTypeBrowserUserInput,
		BrowserID: "browser_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		Kind:      "click",
	})
	if err == nil {
		t.Fatal("expected expired user input to fail")
	}
}

func TestClaimRejectsInvalidOwner(t *testing.T) {
	svc := &fakeService{}
	sent := &recordingSender{}
	manager := NewManager(ManagerOptions{Service: svc, Sender: sent, FrameInterval: time.Hour})
	if err := manager.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	err := manager.Claim(context.Background(), &protocol.BrowserControlClaim{
		Type:      protocol.MsgTypeBrowserControlClaim,
		BrowserID: "browser_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		Owner:     "mallory",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid browser control owner") {
		t.Fatalf("expected invalid owner error, got %v", err)
	}
}

func TestUserInputRejectsStaleControlVersion(t *testing.T) {
	svc := &fakeService{}
	sent := &recordingSender{}
	manager := NewManager(ManagerOptions{Service: svc, Sender: sent, FrameInterval: time.Hour})
	if err := manager.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	err := manager.Claim(context.Background(), &protocol.BrowserControlClaim{
		Type:      protocol.MsgTypeBrowserControlClaim,
		BrowserID: "browser_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		Owner:     protocol.BrowserOwnerLex,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.UserInput(context.Background(), &protocol.BrowserUserInput{
		Type:           protocol.MsgTypeBrowserUserInput,
		InputID:        "input-1",
		BrowserID:      "browser_1",
		SessionID:      "session_1",
		ChannelID:      "channel_1",
		Owner:          protocol.BrowserOwnerLex,
		Kind:           protocol.BrowserInputKindClick,
		ControlVersion: 99,
	})
	if err == nil || !strings.Contains(err.Error(), "stale browser control version") {
		t.Fatalf("expected stale control version error, got %v", err)
	}
	if hasCall(svc.snapshot(), "input:click") {
		t.Fatalf("stale input reached service")
	}
	var foundAck bool
	sentMessages := sent.snapshot()
	for _, msg := range sentMessages {
		ack, ok := msg.(*protocol.BrowserUserInputAck)
		if ok && ack.InputID == "input-1" {
			foundAck = true
			if ack.Accepted || ack.Reason != protocol.BrowserInputRejectStaleFrame {
				t.Fatalf("ack = %+v", ack)
			}
		}
	}
	if !foundAck {
		t.Fatalf("expected stale input ack in %#v", sentMessages)
	}
}

func hasCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
