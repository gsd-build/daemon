// pi-smoke: standalone smoke test for the pi-engine path.
//
// Spawns a session.Actor with a recording relay, dispatches one
// protocol.Task{Engine:"pi"} that asks ask_human, simulates the human
// answer, and prints every relay frame the daemon would send to the
// browser. Validates the full daemon to pi to translator to relay to reducer
// pipeline outside the test framework so you can eyeball the output.
//
// Usage:
//
//	GSD_PI_EXTENSION_PATH=/path/to/internal/pi/extension/index.ts \
//	  go run ./cmd/pi-smoke -answer "purple"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gsd-build/daemon/internal/session"
	protocol "github.com/gsd-build/protocol-go"
)

type printRelay struct {
	mu     sync.Mutex
	frames []any
}

func (r *printRelay) Send(_ context.Context, msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	r.printFrame(msg)
	return nil
}
func (r *printRelay) GetFrames() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]any, len(r.frames))
	copy(out, r.frames)
	return out
}

func (r *printRelay) printFrame(msg any) {
	switch f := msg.(type) {
	case *protocol.TaskStarted:
		fmt.Printf("-> TaskStarted task=%s\n", f.TaskID)
	case *protocol.Stream:
		var inner struct {
			Type    string          `json:"type"`
			Subtype string          `json:"subtype"`
			Event   json.RawMessage `json:"event"`
		}
		_ = json.Unmarshal(f.Event, &inner)
		switch inner.Type {
		case "system":
			fmt.Printf("  [%4d] system{%s}\n", f.SequenceNumber, inner.Subtype)
		case "stream_event":
			var ev struct {
				Type         string `json:"type"`
				ContentBlock struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"content_block"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			_ = json.Unmarshal(inner.Event, &ev)
			switch ev.Type {
			case "content_block_start":
				fmt.Printf("  [%4d] stream_event{content_block_start type=%s name=%q}\n", f.SequenceNumber, ev.ContentBlock.Type, ev.ContentBlock.Name)
			case "content_block_delta":
				if ev.Delta.Type == "text_delta" {
					fmt.Printf("  [%4d] stream_event{text_delta %q}\n", f.SequenceNumber, truncate(ev.Delta.Text, 60))
				} else {
					fmt.Printf("  [%4d] stream_event{%s}\n", f.SequenceNumber, ev.Delta.Type)
				}
			case "content_block_stop":
				fmt.Printf("  [%4d] stream_event{content_block_stop}\n", f.SequenceNumber)
			default:
				fmt.Printf("  [%4d] stream_event{%s}\n", f.SequenceNumber, ev.Type)
			}
		case "user":
			fmt.Printf("  [%4d] user{tool_result}\n", f.SequenceNumber)
		case "result":
			fmt.Printf("  [%4d] result\n", f.SequenceNumber)
		default:
			fmt.Printf("  [%4d] %s\n", f.SequenceNumber, inner.Type)
		}
	case *protocol.Question:
		fmt.Printf("-> Question requestID=%s text=%q\n", f.RequestID, truncate(f.Question, 100))
	case *protocol.TaskComplete:
		fmt.Printf("<- TaskComplete cost=%s input=%d output=%d duration=%dms\n", f.CostUSD, f.InputTokens, f.OutputTokens, f.DurationMs)
	case *protocol.TaskError:
		fmt.Printf("x TaskError %q\n", f.Error)
	default:
		fmt.Printf("  ? %T\n", msg)
	}
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func main() {
	answerFlag := flag.String("answer", "blue", "answer to send to ask_human")
	flag.Parse()
	args := flag.Args()
	prompt := "Use ask_human to ask me my favorite color. Then in one short sentence say my color back."
	if len(args) > 0 {
		prompt = strings.Join(args, " ")
	}

	piExt := os.Getenv("GSD_PI_EXTENSION_PATH")
	if piExt == "" {
		piExt = filepath.Join("internal", "pi", "extension", "index.ts")
	}
	if abs, err := filepath.Abs(piExt); err == nil {
		piExt = abs
	}
	if _, err := os.Stat(piExt); err != nil {
		fmt.Fprintf(os.Stderr, "pi extension not found at %s; set GSD_PI_EXTENSION_PATH\n", piExt)
		os.Exit(2)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cwd, _ := os.Getwd()
	relay := &printRelay{}
	actor, err := session.NewActor(session.Options{
		SessionID:       "pi-smoke",
		CWD:             cwd,
		Relay:           relay,
		Model:           "claude-sonnet-4-6",
		PiExtensionPath: piExt,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "new actor: %v\n", err)
		os.Exit(1)
	}
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "smoke-1",
		SessionID: "pi-smoke",
		ChannelID: "ch-smoke",
		Prompt:    prompt,
		Engine:    "pi",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "send task: %v\n", err)
		os.Exit(1)
	}

	deadline := time.Now().Add(60 * time.Second)
	answered := false
	for time.Now().Before(deadline) {
		for _, f := range relay.GetFrames() {
			if !answered {
				if q, ok := f.(*protocol.Question); ok {
					if err := actor.HandleQuestionResponse(&protocol.QuestionResponse{
						Type:      protocol.MsgTypeQuestionResponse,
						ChannelID: "ch-smoke",
						SessionID: "pi-smoke",
						RequestID: q.RequestID,
						Answer:    *answerFlag,
					}); err != nil {
						fmt.Fprintf(os.Stderr, "answer error: %v\n", err)
					}
					answered = true
				}
			}
			if _, ok := f.(*protocol.TaskComplete); ok {
				return
			}
			if _, ok := f.(*protocol.TaskError); ok {
				os.Exit(3)
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "timed out")
	os.Exit(4)
}
