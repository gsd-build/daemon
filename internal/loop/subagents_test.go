package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/session"
	"github.com/gsd-build/daemon/internal/sockapi"
	protocol "github.com/gsd-build/protocol-go"
)

func TestCreateSubagentChildEmitsStartedStatusToParentSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon/subagents/child" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("authorization"); got != "Bearer machine-token" {
			t.Fatalf("authorization = %q", got)
		}
		var body struct {
			MachineID        string `json:"machineId"`
			ParentSessionID  string `json:"parentSessionId"`
			ParentToolCallID string `json:"parentToolCallId"`
			AgentName        string `json:"agentName"`
			Task             string `json:"task"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.ParentSessionID != "parent-session-1" || body.ParentToolCallID != "tool-subagent-1" {
			t.Fatalf("body = %#v", body)
		}
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{
			"childSessionId":"child-session-1",
			"parentSessionId":"parent-session-1",
			"projectId":"project-1",
			"agent":{
				"name":"scout",
				"description":"Read-only scout",
				"systemPrompt":"Read only.",
				"model":"anthropic/claude-haiku-4-5",
				"tools":["read","grep"]
			}
		}`)
	}))
	defer server.Close()

	parentRelay := newLoopFakeRelay()
	parentActor, err := session.NewActor(session.Options{
		SessionID: "parent-session-1",
		CWD:       t.TempDir(),
		Relay:     parentRelay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer parentActor.Stop()

	d := &Daemon{
		cfg: &config.Config{
			MachineID: "machine-1",
			AuthToken: "machine-token",
			ServerURL: server.URL,
		},
		manager: &mockManager{
			getFn: func(sessionID string) *session.Actor {
				if sessionID == "parent-session-1" {
					return parentActor
				}
				return nil
			},
		},
	}

	resp, err := d.CreateSubagentChild(httptest.NewRequest(http.MethodPost, "/", nil), sockapi.CreateSubagentChildRequest{
		ParentSessionID:  "parent-session-1",
		ParentToolCallID: "tool-subagent-1",
		AgentName:        "scout",
		Task:             "Inspect README",
	})
	if err != nil {
		t.Fatalf("CreateSubagentChild: %v", err)
	}
	if resp.ChildSessionID != "child-session-1" {
		t.Fatalf("child session = %q", resp.ChildSessionID)
	}

	stream := waitForLoopStream(t, parentRelay, time.Second, func(frame *protocol.Stream) bool {
		return frame.SessionID == "parent-session-1"
	})
	if stream.ChannelID != "subagent:child-session-1" {
		t.Fatalf("channel = %q", stream.ChannelID)
	}
	var event map[string]any
	if err := json.Unmarshal(stream.Event, &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	want := map[string]any{
		"type":             "subagent_status",
		"status":           "started",
		"parentSessionId":  "parent-session-1",
		"parentToolCallId": "tool-subagent-1",
		"childSessionId":   "child-session-1",
		"projectId":        "project-1",
		"agentName":        "scout",
		"model":            "anthropic/claude-haiku-4-5",
	}
	for key, value := range want {
		if event[key] != value {
			t.Fatalf("event[%s] = %#v, want %#v in %#v", key, event[key], value, event)
		}
	}
}

func waitForLoopStream(
	t *testing.T,
	r *loopFakeRelay,
	timeout time.Duration,
	predicate func(*protocol.Stream) bool,
) *protocol.Stream {
	t.Helper()
	deadline := time.Now().Add(timeout)

	r.mu.Lock()
	defer r.mu.Unlock()

	for {
		for _, frame := range r.frames {
			stream, ok := frame.(*protocol.Stream)
			if ok && predicate(stream) {
				return stream
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for protocol stream")
		}

		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				r.mu.Lock()
				r.cond.Broadcast()
				r.mu.Unlock()
			case <-done:
			}
		}()
		r.cond.Wait()
		close(done)
		cancel()
	}
}
