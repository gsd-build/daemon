package browser

import (
	"context"
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
