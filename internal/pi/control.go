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
	OK               bool             `json:"ok"`
	Error            string           `json:"error"`
	Reason           string           `json:"reason"`
	Summary          string           `json:"summary"`
	FirstKeptEntryID string           `json:"firstKeptEntryId"`
	ContextUsage     *rawContextUsage `json:"contextUsage"`
}

type rawContextUsage struct {
	Tokens        *int64   `json:"tokens"`
	ContextWindow int64    `json:"contextWindow"`
	Percent       *float64 `json:"percent"`
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

	result, readErr := readControlOutput(stdout, opts.OnEvent)
	stderrBytes, _ := io.ReadAll(stderr)
	waitErr := cmd.Wait()
	if readErr != nil {
		return ControlResult{}, readErr
	}
	if waitErr != nil {
		return ControlResult{}, fmt.Errorf("pi control process failed: %w: %s", waitErr, string(stderrBytes))
	}
	if !result.OK && result.Error != "" {
		return result, errors.New(result.Error)
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

	for scanner.Scan() {
		var frame rawControlFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return ControlResult{}, fmt.Errorf("decode pi control frame: %w", err)
		}
		switch frame.Type {
		case "compaction_start":
			if onEvent != nil {
				onEvent(frame.toEvent(ControlEventCompactionStart))
			}
		case "compaction_end":
			if onEvent != nil {
				onEvent(frame.toEvent(ControlEventCompactionEnd))
			}
		case "control_result", "session_stats":
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

func AutoThresholdPercent(contextWindow int64) float64 {
	if contextWindow <= 0 {
		return 0
	}
	return math.Round((float64(contextWindow-defaultReserveTokens)/float64(contextWindow))*100*10000) / 10000
}
