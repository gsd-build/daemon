package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
)

const (
	defaultReserveTokens    int64 = 16384
	defaultKeepRecentTokens int64 = 20000
	defaultContextWindow    int64 = 200000
	longContextWindow       int64 = 1000000
)

type ControlCommandType string

const (
	ControlCommandCompact         ControlCommandType = "compact"
	ControlCommandGetSessionStats ControlCommandType = "get_session_stats"
)

type ControlEventType string

const (
	ControlEventCompactionStart ControlEventType = "compaction_start"
	ControlEventCompactionEnd   ControlEventType = "compaction_end"
)

type ControlCommand struct {
	Type               ControlCommandType
	CustomInstructions string
}

type ControlOptions struct {
	BinaryPath    string
	CWD           string
	SessionFile   string
	Model         string
	ContextWindow int64
	Command       ControlCommand
	OnEvent       func(ControlEvent)
}

type ContextUsage struct {
	Tokens        *int64
	ContextWindow int64
	Percent       *float64
}

type ControlEvent struct {
	Type             ControlEventType
	Reason           string
	Summary          string
	FirstKeptEntryID string
	ContextUsage     *ContextUsage
	ObservedAt       time.Time
}

type ControlResult struct {
	OK           bool
	Error        string
	ContextUsage *ContextUsage
}

type controlFrame struct {
	Type               string `json:"type"`
	CustomInstructions string `json:"customInstructions,omitempty"`
}

type rawControlFrame struct {
	Type             string           `json:"type"`
	Command          string           `json:"command"`
	Success          *bool            `json:"success"`
	OK               bool             `json:"ok"`
	Error            string           `json:"error"`
	Message          string           `json:"message"`
	Reason           string           `json:"reason"`
	Summary          string           `json:"summary"`
	FirstKeptEntryID string           `json:"firstKeptEntryId"`
	ContextUsage     *rawContextUsage `json:"contextUsage"`
	Data             *rawControlData  `json:"data"`
}

type rawContextUsage struct {
	Tokens        *int64   `json:"tokens"`
	ContextWindow int64    `json:"contextWindow"`
	Percent       *float64 `json:"percent"`
}

type rawControlData struct {
	Summary          string           `json:"summary"`
	FirstKeptEntryID string           `json:"firstKeptEntryId"`
	TokensBefore     *int64           `json:"tokensBefore"`
	TokensAfter      *int64           `json:"tokensAfter"`
	Tokens           *rawTokenUsage   `json:"tokens"`
	ContextUsage     *rawContextUsage `json:"contextUsage"`
	Error            string           `json:"error"`
	Message          string           `json:"message"`
}

type rawTokenUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Total      int64 `json:"total"`
}

func RunControl(ctx context.Context, opts ControlOptions) (ControlResult, error) {
	if opts.BinaryPath == "" {
		return ControlResult{}, errors.New("pi binary path is required")
	}
	if opts.SessionFile == "" {
		return ControlResult{}, errors.New("pi session file is required")
	}

	cmd := piRPCCommand(ctx, opts.BinaryPath, opts.CWD, opts.SessionFile)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ControlResult{}, fmt.Errorf("open pi stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ControlResult{}, fmt.Errorf("open pi stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return ControlResult{}, fmt.Errorf("open pi stderr: %w", err)
	}
	cmd.Env = planCapabilityEnv(os.Environ(), nil)

	if err := cmd.Start(); err != nil {
		return ControlResult{}, fmt.Errorf("start pi control process: %w", err)
	}

	writeErr := writeControlCommand(stdin, opts.Command)
	_ = stdin.Close()
	if writeErr != nil {
		_ = cmd.Wait()
		return ControlResult{}, writeErr
	}

	stderrDone := make(chan struct{})
	var stderrBytes []byte
	var stderrErr error
	go func() {
		defer close(stderrDone)
		stderrBytes, stderrErr = io.ReadAll(stderr)
	}()

	fallbackContextWindow := opts.ContextWindow
	if fallbackContextWindow <= 0 {
		fallbackContextWindow = ContextWindowForModel(opts.Model)
	}

	result, readErr := readControlOutput(stdout, opts.OnEvent, fallbackContextWindow)
	<-stderrDone
	waitErr := cmd.Wait()
	if readErr != nil {
		return ControlResult{}, readErr
	}
	if stderrErr != nil {
		return ControlResult{}, fmt.Errorf("read pi control stderr: %w", stderrErr)
	}
	if waitErr != nil {
		return ControlResult{}, fmt.Errorf("pi control process failed: %w: %s", waitErr, string(stderrBytes))
	}
	if !result.OK {
		if result.Error != "" {
			return result, errors.New(result.Error)
		}
		return result, errors.New("pi control command failed")
	}
	return result, nil
}

func writeControlCommand(w io.Writer, command ControlCommand) error {
	frame := controlFrame{Type: string(command.Type)}
	if command.Type == ControlCommandCompact {
		frame.CustomInstructions = command.CustomInstructions
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal pi control command: %w", err)
	}
	if _, err := w.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write pi control command: %w", err)
	}
	return nil
}

func readControlOutput(r io.Reader, onEvent func(ControlEvent), fallbackContextWindow int64) (ControlResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	result := ControlResult{}
	sawTerminalFrame := false
	sawCompactionStart := false
	sawCompactionEnd := false

	for scanner.Scan() {
		var frame rawControlFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return ControlResult{}, fmt.Errorf("decode pi control frame: %w", err)
		}
		switch frame.Type {
		case "compaction_start":
			sawCompactionStart = true
			if onEvent != nil {
				onEvent(frame.toEvent(ControlEventCompactionStart, fallbackContextWindow))
			}
		case "compaction_end":
			sawCompactionEnd = true
			if onEvent != nil {
				onEvent(frame.toEvent(ControlEventCompactionEnd, fallbackContextWindow))
			}
		case "response":
			terminalResult, ok := frame.toResponseResult(onEvent, !sawCompactionStart, !sawCompactionEnd, fallbackContextWindow)
			if ok {
				if frame.Command == string(ControlCommandCompact) {
					sawCompactionStart = true
					sawCompactionEnd = true
				}
				sawTerminalFrame = true
				result = terminalResult
			}
		case "control_result", "session_stats":
			sawTerminalFrame = true
			result = ControlResult{
				OK:           frame.OK || frame.Error == "",
				Error:        frame.Error,
				ContextUsage: frame.ContextUsage.toContextUsage().withFallbackWindow(fallbackContextWindow),
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return ControlResult{}, fmt.Errorf("read pi control output: %w", err)
	}
	if !sawTerminalFrame {
		return ControlResult{}, errors.New("pi control output missing terminal result frame")
	}
	return result, nil
}

func (frame rawControlFrame) toEvent(eventType ControlEventType, fallbackContextWindow int64) ControlEvent {
	return ControlEvent{
		Type:             eventType,
		Reason:           frame.Reason,
		Summary:          frame.Summary,
		FirstKeptEntryID: frame.FirstKeptEntryID,
		ContextUsage:     frame.ContextUsage.toContextUsage().withFallbackWindow(fallbackContextWindow),
		ObservedAt:       time.Now().UTC(),
	}
}

func (frame rawControlFrame) toResponseResult(onEvent func(ControlEvent), synthesizeStart bool, synthesizeEnd bool, fallbackContextWindow int64) (ControlResult, bool) {
	switch frame.Command {
	case string(ControlCommandGetSessionStats):
		return ControlResult{
			OK:           frame.responseOK(),
			Error:        frame.responseError(),
			ContextUsage: frame.responseContextUsage(fallbackContextWindow),
		}, true
	case string(ControlCommandCompact):
		if frame.responseOK() && onEvent != nil {
			if synthesizeStart {
				onEvent(frame.toResponseCompactionStartEvent(fallbackContextWindow))
			}
			if synthesizeEnd {
				onEvent(frame.toResponseCompactionEndEvent(fallbackContextWindow))
			}
		}
		return ControlResult{
			OK:           frame.responseOK(),
			Error:        frame.responseError(),
			ContextUsage: frame.responseContextUsage(fallbackContextWindow),
		}, true
	default:
		return ControlResult{}, false
	}
}

func (frame rawControlFrame) responseOK() bool {
	if frame.Success != nil {
		return *frame.Success
	}
	return frame.OK || frame.responseError() == ""
}

func (frame rawControlFrame) responseError() string {
	if frame.Error != "" {
		return frame.Error
	}
	if frame.Message != "" {
		return frame.Message
	}
	if frame.Data != nil {
		if frame.Data.Error != "" {
			return frame.Data.Error
		}
		if frame.Data.Message != "" {
			return frame.Data.Message
		}
	}
	return ""
}

func (frame rawControlFrame) responseContextUsage(fallbackContextWindow int64) *ContextUsage {
	if usage := frame.ContextUsage.toContextUsage(); usage != nil {
		return usage.withFallbackWindow(fallbackContextWindow)
	}
	if frame.Data == nil {
		return nil
	}
	if usage := frame.Data.ContextUsage.toContextUsage(); usage != nil {
		return usage.withFallbackWindow(fallbackContextWindow)
	}
	if frame.Command == string(ControlCommandGetSessionStats) {
		return nil
	}
	if usage := frame.Data.Tokens.toContextUsage(fallbackContextWindow); usage != nil {
		return usage
	}
	if frame.Data.TokensAfter != nil {
		return contextUsageFromTokenCount(*frame.Data.TokensAfter, fallbackContextWindow)
	}
	if frame.Data.TokensBefore != nil {
		return contextUsageFromTokenCount(*frame.Data.TokensBefore, fallbackContextWindow)
	}
	return nil
}

func (frame rawControlFrame) toResponseCompactionStartEvent(fallbackContextWindow int64) ControlEvent {
	event := ControlEvent{
		Type:       ControlEventCompactionStart,
		Reason:     frame.Reason,
		ObservedAt: time.Now().UTC(),
	}
	if event.Reason == "" {
		event.Reason = "manual"
	}
	if frame.Data != nil && frame.Data.TokensBefore != nil {
		event.ContextUsage = contextUsageFromTokenCount(*frame.Data.TokensBefore, fallbackContextWindow)
	} else {
		event.ContextUsage = frame.responseContextUsage(fallbackContextWindow)
	}
	return event
}

func (frame rawControlFrame) toResponseCompactionEndEvent(fallbackContextWindow int64) ControlEvent {
	event := ControlEvent{
		Type:       ControlEventCompactionEnd,
		Reason:     frame.Reason,
		ObservedAt: time.Now().UTC(),
	}
	if event.Reason == "" {
		event.Reason = "manual"
	}
	if frame.Data != nil {
		event.Summary = frame.Data.Summary
		event.FirstKeptEntryID = frame.Data.FirstKeptEntryID
		if frame.Data.TokensAfter != nil {
			event.ContextUsage = contextUsageFromTokenCount(*frame.Data.TokensAfter, fallbackContextWindow)
		} else if usage := frame.Data.ContextUsage.toContextUsage(); usage != nil {
			event.ContextUsage = usage.withFallbackWindow(fallbackContextWindow)
		}
	}
	if event.Summary == "" {
		event.Summary = frame.Summary
	}
	if event.FirstKeptEntryID == "" {
		event.FirstKeptEntryID = frame.FirstKeptEntryID
	}
	if event.ContextUsage == nil {
		event.ContextUsage = frame.ContextUsage.toContextUsage().withFallbackWindow(fallbackContextWindow)
	}
	return event
}

func (usage *rawContextUsage) toContextUsage() *ContextUsage {
	if usage == nil {
		return nil
	}
	return &ContextUsage{
		Tokens:        usage.Tokens,
		ContextWindow: usage.ContextWindow,
		Percent:       usage.Percent,
	}
}

func (usage *ContextUsage) withFallbackWindow(contextWindow int64) *ContextUsage {
	if usage == nil {
		return nil
	}
	if contextWindow <= 0 {
		contextWindow = defaultContextWindow
	}
	if usage.ContextWindow <= 0 {
		usage.ContextWindow = contextWindow
	}
	if usage.Percent == nil && usage.Tokens != nil && usage.ContextWindow > 0 {
		percent := (float64(*usage.Tokens) / float64(usage.ContextWindow)) * 100
		usage.Percent = &percent
	}
	return usage
}

func (usage *rawTokenUsage) toContextUsage(fallbackContextWindow int64) *ContextUsage {
	if usage == nil {
		return nil
	}
	return contextUsageFromTokenCount(usage.Total, fallbackContextWindow)
}

func contextUsageFromTokenCount(tokens int64, contextWindow int64) *ContextUsage {
	if contextWindow <= 0 {
		contextWindow = defaultContextWindow
	}
	percent := (float64(tokens) / float64(contextWindow)) * 100
	return &ContextUsage{
		Tokens:        &tokens,
		ContextWindow: contextWindow,
		Percent:       &percent,
	}
}

func ContextWindowForModel(model string) int64 {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(model, "["); i > 0 && strings.HasSuffix(model, "]") {
		model = strings.TrimSpace(model[:i])
	}
	if i := strings.LastIndex(model, ":"); i > 0 {
		model = strings.TrimSpace(model[:i])
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	switch model {
	case "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6":
		return longContextWindow
	default:
		return defaultContextWindow
	}
}

func AutoThresholdPercent(contextWindow int64) float64 {
	if contextWindow <= defaultReserveTokens {
		return 0
	}
	percent := (float64(contextWindow-defaultReserveTokens) / float64(contextWindow)) * 100
	return math.Round(percent*10000) / 10000
}
