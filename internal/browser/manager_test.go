package browser

import (
	"context"
	"errors"
	"testing"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

type fakeService struct {
	calls []string
}

func (f *fakeService) Open(ctx context.Context, req OpenRequest) (OpenResult, error) {
	f.calls = append(f.calls, "open:"+req.GrantID)
	return OpenResult{BrowserID: "browser_1", URL: "about:blank", Title: "Blank"}, nil
}

func (f *fakeService) Close(ctx context.Context, browserID string) error {
	f.calls = append(f.calls, "close:"+browserID)
	return nil
}

func (f *fakeService) Frame(ctx context.Context, browserID string) (Frame, error) {
	f.calls = append(f.calls, "frame:"+browserID)
	return Frame{
		Sequence:    1,
		ContentType: "image/jpeg",
		DataBase64:  "aGVsbG8=",
		Width:       1280,
		Height:      720,
		CapturedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func (f *fakeService) Tool(ctx context.Context, browserID string, method string, params []byte) (ToolResult, error) {
	f.calls = append(f.calls, "tool:"+method)
	return ToolResult{OK: true, ResultJSON: []byte(`{"ok":true}`)}, nil
}

func (f *fakeService) UserInput(ctx context.Context, browserID string, input *protocol.BrowserUserInput) error {
	f.calls = append(f.calls, "input:"+input.Kind)
	return nil
}

type recordingSender struct {
	msgs []any
}

func (r *recordingSender) Send(ctx context.Context, msg any) error {
	r.msgs = append(r.msgs, msg)
	return nil
}

type failingSender struct{}

func (failingSender) Send(ctx context.Context, msg any) error {
	return errors.New("send failed")
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
	if !hasCall(svc.calls, "close:browser_1") {
		t.Fatalf("expected browser close call, got %v", svc.calls)
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
	if !hasCall(svc.calls, "close:browser_1") {
		t.Fatalf("expected browser close call, got %v", svc.calls)
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

func hasCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
