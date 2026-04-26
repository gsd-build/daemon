// pi-smoke runs the pi executor without session actor wiring.
//
// It starts internal/pi.NewExecutor directly, answers ask_human requests with
// a flag value, and prints the Claude-shaped events emitted by the translator.
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
	"time"

	"github.com/gsd-build/daemon/internal/claude"
	"github.com/gsd-build/daemon/internal/pi"
)

func main() {
	answerFlag := flag.String("answer", "blue", "answer to send to ask_human")
	binaryFlag := flag.String("pi", "pi", "path to the pi binary")
	modelFlag := flag.String("model", "claude-sonnet-4-6", "model id passed to pi")
	providerFlag := flag.String("provider", "claude-cli", "pi provider name")
	timeoutFlag := flag.Duration("timeout", 90*time.Second, "smoke run timeout")
	flag.Parse()

	prompt := "Use ask_human to ask me my favorite color. Then in one short sentence say my color back."
	if args := flag.Args(); len(args) > 0 {
		prompt = strings.Join(args, " ")
	}

	extensionPath := os.Getenv("GSD_PI_EXTENSION_PATH")
	if extensionPath == "" {
		extensionPath = filepath.Join("internal", "pi", "extension", "index.ts")
	}
	if abs, err := filepath.Abs(extensionPath); err == nil {
		extensionPath = abs
	}
	if _, err := os.Stat(extensionPath); err != nil {
		fmt.Fprintf(os.Stderr, "pi extension not found at %s; set GSD_PI_EXTENSION_PATH\n", extensionPath)
		os.Exit(2)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "get cwd: %v\n", err)
		os.Exit(1)
	}

	executor := pi.NewExecutor(pi.Options{
		BinaryPath:    *binaryFlag,
		CWD:           cwd,
		Model:         *modelFlag,
		Provider:      *providerFlag,
		ExtensionPath: extensionPath,
		TaskID:        "smoke-1",
		Prompt:        prompt,
	})

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	var sequence int
	err = executor.Run(ctx, func(event claude.Event) error {
		sequence++
		printEvent(sequence, event)
		return nil
	}, func(ctx context.Context, req pi.UIRequest) (string, error) {
		fmt.Printf("-> ui_request id=%s method=%s title=%q\n", req.ID, req.Method, truncate(req.Title, 100))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			return *answerFlag, nil
		}
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pi smoke failed: %v\n", err)
		os.Exit(1)
	}
	if ctx.Err() == context.DeadlineExceeded {
		fmt.Fprintf(os.Stderr, "pi smoke timed out after %s\n", timeoutFlag.String())
		os.Exit(4)
	}
}

func printEvent(sequence int, event claude.Event) {
	var top struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Event   json.RawMessage `json:"event"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal(event.Raw, &top)

	switch top.Type {
	case "system":
		fmt.Printf("  [%4d] system{%s}\n", sequence, top.Subtype)
	case "stream_event":
		printStreamEvent(sequence, top.Event)
	case "assistant":
		fmt.Printf("  [%4d] assistant{%s}\n", sequence, summarizeContent(top.Message.Content))
	case "user":
		fmt.Printf("  [%4d] user{tool_result}\n", sequence)
	case "result":
		fmt.Printf("  [%4d] result\n", sequence)
	default:
		fmt.Printf("  [%4d] %s\n", sequence, top.Type)
	}
}

func printStreamEvent(sequence int, raw json.RawMessage) {
	var event struct {
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
	_ = json.Unmarshal(raw, &event)

	switch event.Type {
	case "content_block_start":
		fmt.Printf("  [%4d] stream_event{content_block_start type=%s name=%q}\n", sequence, event.ContentBlock.Type, event.ContentBlock.Name)
	case "content_block_delta":
		if event.Delta.Type == "text_delta" {
			fmt.Printf("  [%4d] stream_event{text_delta %q}\n", sequence, truncate(event.Delta.Text, 60))
			return
		}
		fmt.Printf("  [%4d] stream_event{%s}\n", sequence, event.Delta.Type)
	case "content_block_stop":
		fmt.Printf("  [%4d] stream_event{content_block_stop}\n", sequence)
	default:
		fmt.Printf("  [%4d] stream_event{%s}\n", sequence, event.Type)
	}
}

func summarizeContent(content []struct {
	Type string `json:"type"`
	Name string `json:"name"`
}) string {
	parts := make([]string, 0, len(content))
	for _, block := range content {
		if block.Name != "" {
			parts = append(parts, block.Type+":"+block.Name)
			continue
		}
		parts = append(parts, block.Type)
	}
	return strings.Join(parts, ",")
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
