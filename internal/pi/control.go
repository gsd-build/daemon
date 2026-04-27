package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

const (
	defaultReserveTokens    int64 = 16384
	defaultKeepRecentTokens int64 = 20000
	defaultContextWindow    int64 = 200000
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
	BinaryPath  string
	CWD         string
	SessionFile string
	Command     ControlCommand
	OnEvent     func(ControlEvent)
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

	result, readErr := readControlOutput(stdout, opts.OnEvent)
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

func readControlOutput(r io.Reader, onEvent func(ControlEvent)) (ControlResult, error) {
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
				onEvent(frame.toEvent(ControlEventCompactionStart))
			}
		case "compaction_end":
			sawCompactionEnd = true
			if onEvent != nil {
				onEvent(frame.toEvent(ControlEventCompactionEnd))
			}
		case "response":
			terminalResult, ok := frame.toResponseResult(onEvent, !sawCompactionStart, !sawCompactionEnd)
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
				ContextUsage: frame.ContextUsage.toContextUsage(),
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

func (frame rawControlFrame) toEvent(eventType ControlEventType) ControlEvent {
	return ControlEvent{
		Type:             eventType,
		Reason:           frame.Reason,
		Summary:          frame.Summary,
		FirstKeptEntryID: frame.FirstKeptEntryID,
		ContextUsage:     frame.ContextUsage.toContextUsage(),
		ObservedAt:       time.Now().UTC(),
	}
}

func (frame rawControlFrame) toResponseResult(onEvent func(ControlEvent), synthesizeStart bool, synthesizeEnd bool) (ControlResult, bool) {
	switch frame.Command {
	case string(ControlCommandGetSessionStats):
		return ControlResult{
			OK:           frame.responseOK(),
			Error:        frame.responseError(),
			ContextUsage: frame.responseContextUsage(),
		}, true
	case string(ControlCommandCompact):
		if frame.responseOK() && onEvent != nil {
			if synthesizeStart {
				onEvent(frame.toResponseCompactionStartEvent())
			}
			if synthesizeEnd {
				onEvent(frame.toResponseCompactionEndEvent())
			}
		}
		return ControlResult{
			OK:           frame.responseOK(),
			Error:        frame.responseError(),
			ContextUsage: frame.responseContextUsage(),
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

func (frame rawControlFrame) responseContextUsage() *ContextUsage {
	if usage := frame.ContextUsage.toContextUsage(); usage != nil {
		return usage.withFallbackWindow(defaultContextWindow)
	}
	if frame.Data == nil {
		return nil
	}
	if usage := frame.Data.ContextUsage.toContextUsage(); usage != nil {
		return usage.withFallbackWindow(defaultContextWindow)
	}
	if usage := frame.Data.Tokens.toContextUsage(); usage != nil {
		return usage
	}
	if frame.Data.TokensAfter != nil {
		return contextUsageFromTokenCount(*frame.Data.TokensAfter)
	}
	if frame.Data.TokensBefore != nil {
		return contextUsageFromTokenCount(*frame.Data.TokensBefore)
	}
	return nil
}

func (frame rawControlFrame) toResponseCompactionStartEvent() ControlEvent {
	event := ControlEvent{
		Type:       ControlEventCompactionStart,
		Reason:     frame.Reason,
		ObservedAt: time.Now().UTC(),
	}
	if event.Reason == "" {
		event.Reason = "manual"
	}
	if frame.Data != nil && frame.Data.TokensBefore != nil {
		event.ContextUsage = contextUsageFromTokenCount(*frame.Data.TokensBefore)
	} else {
		event.ContextUsage = frame.responseContextUsage()
	}
	return event
}

func (frame rawControlFrame) toResponseCompactionEndEvent() ControlEvent {
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
			event.ContextUsage = contextUsageFromTokenCount(*frame.Data.TokensAfter)
		} else if usage := frame.Data.ContextUsage.toContextUsage(); usage != nil {
			event.ContextUsage = usage.withFallbackWindow(defaultContextWindow)
		}
	}
	if event.Summary == "" {
		event.Summary = frame.Summary
	}
	if event.FirstKeptEntryID == "" {
		event.FirstKeptEntryID = frame.FirstKeptEntryID
	}
	if event.ContextUsage == nil {
		event.ContextUsage = frame.ContextUsage.toContextUsage().withFallbackWindow(defaultContextWindow)
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
	if usage.ContextWindow > 0 || contextWindow <= 0 {
		return usage
	}
	usage.ContextWindow = contextWindow
	if usage.Percent == nil && usage.Tokens != nil {
		percent := (float64(*usage.Tokens) / float64(contextWindow)) * 100
		usage.Percent = &percent
	}
	return usage
}

func (usage *rawTokenUsage) toContextUsage() *ContextUsage {
	if usage == nil {
		return nil
	}
	return contextUsageFromTokenCount(usage.Total)
}

func contextUsageFromTokenCount(tokens int64) *ContextUsage {
	percent := (float64(tokens) / float64(defaultContextWindow)) * 100
	return &ContextUsage{
		Tokens:        &tokens,
		ContextWindow: defaultContextWindow,
		Percent:       &percent,
	}
}

func AutoThresholdPercent(contextWindow int64) float64 {
	if contextWindow <= defaultReserveTokens {
		return 0
	}
	percent := (float64(contextWindow-defaultReserveTokens) / float64(contextWindow)) * 100
	return math.Round(percent*10000) / 10000
}
