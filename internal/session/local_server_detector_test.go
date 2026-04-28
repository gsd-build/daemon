package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
	protocol "github.com/gsd-build/protocol-go"
)

func TestExtractLocalServerCandidatesFromToolOutput(t *testing.T) {
	candidates := extractLocalServerCandidates("Local: http://localhost:5173/\nNetwork: http://192.168.1.4:5173/")
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	if candidates[0].port != 5173 {
		t.Fatalf("port = %d, want 5173", candidates[0].port)
	}
	if candidates[0].url != "http://127.0.0.1:5173/" {
		t.Fatalf("url = %q", candidates[0].url)
	}
}

func TestActorReportsVerifiedLocalServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	relay := newFakeRelay()
	actor := &Actor{
		opts: Options{
			SessionID: "sess-preview",
			Relay:     relay,
		},
		now: func() time.Time {
			return time.Date(2026, 4, 27, 20, 0, 0, 0, time.UTC)
		},
	}
	tc := &taskContext{TaskID: "task-preview", ChannelID: "ch-preview"}
	detector := newLocalServerDetector()

	actor.maybeReportLocalServers(context.Background(), tc, detector, mustJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{{
				"type":  "tool_use",
				"id":    "toolu-preview",
				"name":  "Bash",
				"input": map[string]any{"command": "pnpm dev"},
			}},
		},
	}))
	actor.maybeReportLocalServers(context.Background(), tc, detector, mustJSON(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": "toolu-preview",
				"content":     fmt.Sprintf("ready on http://localhost:%d/", port),
			}},
		},
	}))

	ok := relay.waitFor(t, 3*time.Second, func(frames []any) bool {
		for _, frame := range frames {
			if detected, ok := frame.(*protocol.LocalServerDetected); ok {
				return detected.SessionID == "sess-preview" &&
					detected.ChannelID == "ch-preview" &&
					detected.TaskID == "task-preview" &&
					detected.ToolUseID == "toolu-preview" &&
					detected.Port == port &&
					detected.Command == "pnpm dev" &&
					detected.Source == localServerDetectionSource
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("local server detection frame missing: %#v", relay.GetFrames())
	}
}

func TestLocalServerDetectionOutlivesTaskContext(t *testing.T) {
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	var startOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		startOnce.Do(func() {
			close(probeStarted)
		})
		<-releaseProbe
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	relay := newFakeRelay()
	actor := &Actor{
		opts: Options{
			SessionID: "sess-preview",
			Relay:     relay,
		},
		now: time.Now,
	}
	tc := &taskContext{TaskID: "task-preview", ChannelID: "ch-preview"}
	taskCtx, cancelTask := context.WithCancel(context.Background())

	_, err = actor.forwardExecutorEvents(context.Background(), taskCtx, tc, func(_ context.Context, onEvent func(claude.Event) error) error {
		if err := onEvent(claude.Event{Type: "assistant", Raw: mustJSON(t, map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type":  "tool_use",
					"id":    "toolu-preview",
					"name":  "Bash",
					"input": map[string]any{"command": "pnpm dev"},
				}},
			},
		})}); err != nil {
			return err
		}
		if err := onEvent(claude.Event{Type: "user", Raw: mustJSON(t, map[string]any{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": "toolu-preview",
					"content":     fmt.Sprintf("ready on http://localhost:%d/", port),
				}},
			},
		})}); err != nil {
			return err
		}
		return onEvent(claude.Event{Type: "result", Raw: mustJSON(t, map[string]any{"type": "result"})})
	})
	if err != nil {
		t.Fatalf("forward events: %v", err)
	}

	select {
	case <-probeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("local server probe did not start")
	}
	cancelTask()
	close(releaseProbe)

	ok := relay.waitFor(t, 3*time.Second, func(frames []any) bool {
		for _, frame := range frames {
			if detected, ok := frame.(*protocol.LocalServerDetected); ok {
				return detected.Port == port
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("local server detection frame missing after task cancellation: %#v", relay.GetFrames())
	}
}

func TestLocalServerDetectionRetriesAfterSendFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}

	relay := newFailOnceRelay()
	actor := &Actor{
		opts: Options{
			SessionID: "sess-preview",
			Relay:     relay,
		},
		now: time.Now,
	}
	tc := &taskContext{TaskID: "task-preview", ChannelID: "ch-preview"}
	detector := newLocalServerDetector()
	toolUse := mustJSON(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{{
				"type":  "tool_use",
				"id":    "toolu-preview",
				"name":  "Bash",
				"input": map[string]any{"command": "pnpm dev"},
			}},
		},
	})
	toolResult := mustJSON(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": "toolu-preview",
				"content":     fmt.Sprintf("ready on http://localhost:%d/", port),
			}},
		},
	})

	actor.maybeReportLocalServers(context.Background(), tc, detector, toolUse)
	actor.maybeReportLocalServers(context.Background(), tc, detector, toolResult)

	key := localServerKey(tc.ChannelID, port)
	deleted := waitUntil(3*time.Second, func() bool {
		_, ok := actor.localServerDetections.Load(key)
		return !ok
	})
	if !deleted {
		t.Fatal("dedupe entry was not cleared after send failure")
	}

	actor.maybeReportLocalServers(context.Background(), tc, detector, toolResult)
	ok := relay.waitFor(t, 3*time.Second, func(frames []any) bool {
		for _, frame := range frames {
			if detected, ok := frame.(*protocol.LocalServerDetected); ok {
				return detected.Port == port
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("local server detection frame missing after retry: %#v", relay.GetFrames())
	}
}

type failOnceRelay struct {
	*fakeRelay
	mu     sync.Mutex
	failed bool
}

func newFailOnceRelay() *failOnceRelay {
	return &failOnceRelay{fakeRelay: newFakeRelay()}
}

func (r *failOnceRelay) Send(ctx context.Context, msg any) error {
	r.mu.Lock()
	if !r.failed {
		r.failed = true
		r.mu.Unlock()
		return errors.New("send failed")
	}
	r.mu.Unlock()
	return r.fakeRelay.Send(ctx, msg)
}

func waitUntil(timeout time.Duration, predicate func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return predicate()
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
