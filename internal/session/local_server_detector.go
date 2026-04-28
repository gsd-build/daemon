package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

const localServerDetectionSource = "tool_output"

var (
	localServerURLPattern  = regexp.MustCompile(`(?i)\bhttps?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(?::([0-9]{1,5}))?(?:/[^\s'"<>)]*)?`)
	localServerHostPattern = regexp.MustCompile(`(?i)\b(?:localhost|127\.0\.0\.1|0\.0\.0\.0):([0-9]{1,5})\b`)
)

type localServerCandidate struct {
	port      int
	url       string
	toolUseID string
	command   string
}

type localServerDetector struct {
	mu       sync.Mutex
	commands map[string]string
}

func newLocalServerDetector() *localServerDetector {
	return &localServerDetector{commands: make(map[string]string)}
}

func (d *localServerDetector) observe(raw json.RawMessage) []localServerCandidate {
	var envelope struct {
		Type  string `json:"type"`
		Event struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
		} `json:"event"`
		Message struct {
			Content []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
				Content   any             `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if envelope.Type == "stream_event" &&
		envelope.Event.Type == "content_block_start" &&
		envelope.Event.ContentBlock.Type == "tool_use" {
		d.recordToolUseLocked(
			envelope.Event.ContentBlock.ID,
			envelope.Event.ContentBlock.Name,
			envelope.Event.ContentBlock.Input,
		)
	}

	var candidates []localServerCandidate
	for _, block := range envelope.Message.Content {
		switch block.Type {
		case "tool_use":
			d.recordToolUseLocked(block.ID, block.Name, block.Input)
		case "tool_result":
			command := d.commands[block.ToolUseID]
			if command == "" {
				continue
			}
			text := toolResultText(block.Content)
			for _, candidate := range extractLocalServerCandidates(text) {
				candidate.toolUseID = block.ToolUseID
				candidate.command = command
				candidates = append(candidates, candidate)
			}
		}
	}
	return candidates
}

func (d *localServerDetector) recordToolUseLocked(toolUseID string, name string, input json.RawMessage) {
	if toolUseID == "" || name != "Bash" {
		return
	}
	var payload struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &payload); err != nil || strings.TrimSpace(payload.Command) == "" {
		return
	}
	d.commands[toolUseID] = strings.TrimSpace(payload.Command)
}

func toolResultText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			switch part := item.(type) {
			case string:
				b.WriteString(part)
				b.WriteByte('\n')
			case map[string]any:
				if text, ok := part["text"].(string); ok {
					b.WriteString(text)
					b.WriteByte('\n')
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func extractLocalServerCandidates(text string) []localServerCandidate {
	seen := make(map[int]bool)
	var out []localServerCandidate
	add := func(port int, rawURL string) {
		if port < 1 || port > 65535 || seen[port] {
			return
		}
		seen[port] = true
		if rawURL == "" {
			rawURL = fmt.Sprintf("http://127.0.0.1:%d/", port)
		}
		out = append(out, localServerCandidate{port: port, url: normalizeLocalServerURL(rawURL, port)})
	}

	for _, match := range localServerURLPattern.FindAllStringSubmatch(text, -1) {
		rawURL := match[0]
		port := 0
		if match[1] != "" {
			port, _ = strconv.Atoi(match[1])
		} else if parsed, err := url.Parse(rawURL); err == nil {
			switch strings.ToLower(parsed.Scheme) {
			case "http":
				port = 80
			case "https":
				port = 443
			}
		}
		add(port, rawURL)
	}

	for _, match := range localServerHostPattern.FindAllStringSubmatch(text, -1) {
		port, _ := strconv.Atoi(match[1])
		add(port, "")
	}
	return out
}

func normalizeLocalServerURL(rawURL string, port int) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return fmt.Sprintf("http://127.0.0.1:%d/", port)
	}
	parsed.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String()
}

func (a *Actor) maybeReportLocalServers(ctx context.Context, tc *taskContext, detector *localServerDetector, raw json.RawMessage) {
	for _, candidate := range detector.observe(raw) {
		if !a.localServerDetections.CompareAndSwap(localServerKey(tc.ChannelID, candidate.port), nil, true) {
			if _, loaded := a.localServerDetections.LoadOrStore(localServerKey(tc.ChannelID, candidate.port), true); loaded {
				continue
			}
		}
		go a.verifyAndReportLocalServer(ctx, tc, candidate)
	}
}

func localServerKey(channelID string, port int) string {
	return fmt.Sprintf("%s:%d", channelID, port)
}

func (a *Actor) verifyAndReportLocalServer(ctx context.Context, tc *taskContext, candidate localServerCandidate) {
	if !probeLocalServer(ctx, candidate.port) {
		a.localServerDetections.Delete(localServerKey(tc.ChannelID, candidate.port))
		return
	}

	detectedAt := a.currentTime().UTC()
	sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.opts.Relay.Send(sendCtx, &protocol.LocalServerDetected{
		Type:       protocol.MsgTypeLocalServerDetected,
		SessionID:  a.opts.SessionID,
		ChannelID:  tc.ChannelID,
		TaskID:     tc.TaskID,
		ToolUseID:  candidate.toolUseID,
		Host:       "127.0.0.1",
		Port:       candidate.port,
		URL:        candidate.url,
		Command:    candidate.command,
		Source:     localServerDetectionSource,
		DetectedAt: detectedAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		slog.Warn("local server detection send failed", "sessionId", a.opts.SessionID, "port", candidate.port, "err", err)
	}
}

func probeLocalServer(ctx context.Context, port int) bool {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	for attempt := 0; attempt < 8; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
	return false
}
