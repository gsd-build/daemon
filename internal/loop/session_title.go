package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
	"github.com/gsd-build/daemon/internal/pi"
	protocol "github.com/gsd-build/protocol-go"
)

const (
	sessionTitleTimeout   = 8 * time.Second
	sessionTitleModel     = "claude-sonnet-4-6"
	sessionTitleMaxInput  = 4000
	sessionTitleMaxLength = 60
)

type sessionTitleGenerator func(context.Context, sessionTitleGenerateRequest) (string, error)

type sessionTitleGenerateRequest struct {
	FirstMessage    string
	CWD             string
	Model           string
	PiBinaryPath    string
	PiExtensionPath string
}

func (d *Daemon) handleSessionTitleRequest(msg *protocol.SessionTitleRequest) error {
	go d.generateAndSendSessionTitle(msg)
	return nil
}

func (d *Daemon) generateAndSendSessionTitle(msg *protocol.SessionTitleRequest) {
	generator := d.generateSessionTitle
	if generator == nil {
		generator = defaultSessionTitleGenerator
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionTitleTimeout)
	defer cancel()

	title, err := generator(ctx, sessionTitleGenerateRequest{
		FirstMessage:    msg.FirstMessage,
		CWD:             d.scopeRootForChannel(msg.ChannelID),
		Model:           msg.Model,
		PiBinaryPath:    d.piBinaryPath,
		PiExtensionPath: d.piExtensionPath,
	})

	result := &protocol.SessionTitleResult{
		Type:         protocol.MsgTypeSessionTitleResult,
		SessionID:    msg.SessionID,
		ChannelID:    msg.ChannelID,
		RequestID:    msg.RequestID,
		ExpectedName: msg.ExpectedName,
		Source:       "pi",
	}
	if err != nil {
		result.Error = err.Error()
	} else if title == "" {
		result.Error = "empty title"
	} else {
		result.OK = true
		result.Title = title
	}

	sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer sendCancel()
	_ = d.client.Send(sendCtx, result)
}

func defaultSessionTitleGenerator(ctx context.Context, req sessionTitleGenerateRequest) (string, error) {
	firstMessage := strings.TrimSpace(req.FirstMessage)
	if firstMessage == "" {
		return "", errors.New("first message is empty")
	}

	binaryPath := req.PiBinaryPath
	if binaryPath == "" {
		binaryPath = "pi"
	}
	extensionPath := req.PiExtensionPath
	if extensionPath == "" {
		extensionPath = defaultPiExtensionPath()
	}

	collector := &sessionTitleCollector{}
	exec := pi.NewExecutor(pi.Options{
		BinaryPath:    binaryPath,
		CWD:           req.CWD,
		Model:         sessionTitleGenerationModel(req.Model),
		ResumeSession: "",
		TaskID:        "session-title",
		Prompt:        buildSessionTitlePrompt(firstMessage),
		ExtensionPath: extensionPath,
		Provider:      "claude-cli",
	})
	if err := exec.Run(ctx, func(event claude.Event) error {
		collector.Observe(event)
		return nil
	}, nil); err != nil {
		return "", err
	}

	title := sanitizeSessionTitle(collector.Text())
	if title == "" {
		return "", errors.New("title generation returned no text")
	}
	return title, nil
}

func sessionTitleGenerationModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return sessionTitleModel
	}
	if strings.Contains(model, "[") && strings.HasSuffix(model, "]") {
		model = strings.TrimSpace(model[:strings.LastIndex(model, "[")])
	}
	return model
}

func buildSessionTitlePrompt(firstMessage string) string {
	input := strings.TrimSpace(firstMessage)
	if len(input) > sessionTitleMaxInput {
		input = input[:sessionTitleMaxInput]
	}
	return fmt.Sprintf(
		"Name this chat from the first user message. Return only a specific title, 2-6 words, no quotes, no ending punctuation.\n\nFirst user message:\n%s",
		input,
	)
}

func sanitizeSessionTitle(title string) string {
	cleaned := strings.Join(strings.Fields(title), " ")
	cleaned = strings.Trim(cleaned, "\"'")
	cleaned = strings.TrimRight(cleaned, ".!?")
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) <= sessionTitleMaxLength {
		return cleaned
	}

	truncated := cleaned[:sessionTitleMaxLength]
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > 0 {
		truncated = truncated[:lastSpace]
	}
	return strings.TrimSpace(truncated)
}

type sessionTitleCollector struct {
	snapshots []string
	deltas    []string
}

func (c *sessionTitleCollector) Observe(event claude.Event) {
	switch event.Type {
	case "assistant":
		if text := assistantSnapshotText(event.Raw); text != "" {
			c.snapshots = append(c.snapshots, text)
		}
	case "stream_event":
		if text := streamDeltaText(event.Raw); text != "" {
			c.deltas = append(c.deltas, text)
		}
	}
}

func (c *sessionTitleCollector) Text() string {
	if len(c.snapshots) > 0 {
		return strings.Join(c.snapshots, " ")
	}
	return strings.Join(c.deltas, "")
}

func assistantSnapshotText(raw json.RawMessage) string {
	var event struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return ""
	}
	var parts []string
	for _, block := range event.Message.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func streamDeltaText(raw json.RawMessage) string {
	var event struct {
		Event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		} `json:"event"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return ""
	}
	if event.Event.Type != "content_block_delta" || event.Event.Delta.Type != "text_delta" {
		return ""
	}
	return event.Event.Delta.Text
}
