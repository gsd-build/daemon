// Package pi owns daemon execution through the pi CLI.
//
// The executor starts one `pi -p --mode rpc` process per task, sends the
// task prompt as the first RPC frame, translates pi events into Claude
// stream-json events, and routes pi UI requests through the daemon session
// actor.
package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
	protocol "github.com/gsd-build/protocol-go"
)

// piExitError wraps pi's exit code + stderr into a user-friendly error.
// Detects the "extension dependencies missing" signature and returns a
// repair hint instead of the raw npm-shaped failure that confuses users.
func piExitError(code int, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	// Specific known failure: pi extension's node_modules is missing or
	// missing the platform-native @anthropic-ai/claude-agent-sdk binary.
	// Surfaces as either "Cannot find module '@anthropic-ai/claude-agent-sdk'"
	// or "Unknown provider \"claude-cli\"" (the second is the symptom; the
	// first is the cause when both appear together).
	if strings.Contains(stderr, "Cannot find module '@anthropic-ai/claude-agent-sdk'") ||
		(strings.Contains(stderr, "Unknown provider") && strings.Contains(stderr, "claude-cli")) {
		return fmt.Errorf(
			"pi extension dependencies are missing — restart the daemon "+
				"(self-heal will run npm ci) or run `gsd-cloud doctor` to diagnose. "+
				"Raw error: %s",
			stderr,
		)
	}
	if strings.Contains(stderr, "Unknown provider") {
		return fmt.Errorf("pi provider is not registered by the daemon extension. Raw error: %s", stderr)
	}
	if stderr != "" {
		return fmt.Errorf("pi exited with code %d: %s", code, stderr)
	}
	return fmt.Errorf("pi exited with code %d (no stderr)", code)
}

// Options configures a pi process.
type Options struct {
	BinaryPath         string // pi binary; defaults to "pi"
	CWD                string
	Model              string // forwarded as --model
	ResumeSession      string // forwarded as --session <path>; empty = --no-session
	TaskID             string
	Prompt             string
	CustomInstructions string
	ExtensionPath      string // forwarded as -e <path>
	Provider           string // forwarded as --provider <name>
	SkillPaths         []string
	BrowserGrantID     string
	BrowserID          string
	BrowserSessionID   string
	PlanCapability     *protocol.PlanCapability
}

// Executor spawns one `pi -p --mode rpc` process per task.
type Executor struct {
	opts                 Options
	OnPIDStart           func(pid int)
	OnPIDExit            func(pid int)
	OnToolExecutionStart func(ToolExecutionStart)
	OnToolExecutionEnd   func(ToolExecutionEnd)
}

// NewExecutor constructs an Executor. Call Run to spawn.
func NewExecutor(opts Options) *Executor {
	if opts.BinaryPath == "" {
		opts.BinaryPath = "pi"
	}
	if strings.TrimSpace(opts.Provider) == "" {
		opts.Provider = "claude-cli"
	}
	return &Executor{opts: opts}
}

func piRPCCommand(ctx context.Context, binaryPath string, cwd string, sessionFile string, args ...string) *exec.Cmd {
	baseArgs := []string{"-p", "--mode", "rpc"}
	baseArgs = append(baseArgs, args...)
	if sessionFile != "" {
		baseArgs = append(baseArgs, "--session", sessionFile)
	} else {
		baseArgs = append(baseArgs, "--no-session")
	}
	cmd := exec.CommandContext(ctx, binaryPath, baseArgs...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	return cmd
}

// UIRequest is a question or prompt the agent issued through pi's UI APIs.
type UIRequest struct {
	ID          string
	Method      string // "input" | "confirm" | "select"
	Title       string
	Placeholder string
}

// UIRequestHandler resolves a UIRequest to a string answer. Returning an
// empty string means "user cancelled". Returning an error aborts the task.
// Implementations typically wait on a questionResponse channel and return
// when ctx is cancelled.
type UIRequestHandler func(context.Context, UIRequest) (string, error)

type ToolExecutionStart struct {
	ToolCallID string
	ToolName   string
	Args       map[string]any
}

// Run spawns pi, sends the prompt over stdin as an RPC `prompt` frame, and
// streams events to onEvent. Blocks until pi exits or ctx is cancelled.
//
// If onUIRequest is non-nil, extension_ui_request events from pi (emitted by
// tools that call ctx.ui.input, e.g. ask_human) are routed to it; the returned
// string is written back as extension_ui_response and pi resumes the tool.
// onUIRequest is called synchronously on the parser goroutine; pi is blocked
// on the answer, so the handler can take as long as it needs.
//
// onEvent receives claude.Event in the claude stream-json shape (translated
// from pi NDJSON). The existing relay forwarding path consumes this unmodified.
func (e *Executor) Run(ctx context.Context, onEvent func(claude.Event) error, onUIRequest UIRequestHandler) error {
	if e.opts.ExtensionPath == "" {
		return fmt.Errorf("pi extension path is required")
	}
	if _, err := os.Stat(e.opts.ExtensionPath); err != nil {
		return fmt.Errorf("pi extension not found at %s: %w", e.opts.ExtensionPath, err)
	}

	args := []string{
		"-e", e.opts.ExtensionPath,
		"--provider", e.opts.Provider,
		"--no-extensions", "--no-prompt-templates",
		"--offline",
	}
	if e.opts.Model != "" {
		args = append(args, "--model", e.opts.Model)
	}
	if customInstructions := strings.TrimSpace(e.opts.CustomInstructions); customInstructions != "" {
		args = append(args, "--append-system-prompt", customInstructions)
	}
	for _, path := range e.opts.SkillPaths {
		if path != "" {
			args = append(args, "--skill", path)
		}
	}

	slog.Info("starting pi",
		"binary", e.opts.BinaryPath,
		"dir", e.opts.CWD,
		"model", e.opts.Model,
		"provider", e.opts.Provider,
		"extension", e.opts.ExtensionPath,
		"skillCount", len(e.opts.SkillPaths),
		"promptLen", len(e.opts.Prompt),
		"customInstructionsLen", len(strings.TrimSpace(e.opts.CustomInstructions)),
		"planCapability", e.opts.PlanCapability != nil,
	)

	cmd := piRPCCommand(ctx, e.opts.BinaryPath, e.opts.CWD, e.opts.ResumeSession, args...)
	cmd.Env = planCapabilityEnv(
		browserEnv(os.Environ(), e.opts.BrowserGrantID, e.opts.BrowserID, e.opts.BrowserSessionID),
		e.opts.PlanCapability,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("start pi: %w", err)
	}

	pid := cmd.Process.Pid
	if e.OnPIDStart != nil {
		e.OnPIDStart(pid)
	}
	defer func() {
		if e.OnPIDExit != nil {
			e.OnPIDExit(pid)
		}
	}()

	// If ctx is cancelled before normal cleanup runs, terminate the whole
	// process group. exec.CommandContext only signals the leader PID, which
	// leaves pi's children reparented to PID 1.
	// Setpgid above means we can hit the group with -pid.
	cleanupDone := make(chan struct{})
	defer close(cleanupDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			select {
			case <-cleanupDone:
				return
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		case <-cleanupDone:
			return
		}
	}()

	// Drain stderr in background so it doesn't fill the pipe buffer.
	stderrBuf := make([]byte, 0, 8*1024)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				stderrBuf = append(stderrBuf, buf[:n]...)
				if len(stderrBuf) > 16*1024 {
					stderrBuf = stderrBuf[len(stderrBuf)-16*1024:]
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Send the prompt as the first RPC frame.
	promptFrame, _ := json.Marshal(map[string]any{
		"id":      "task-prompt",
		"type":    "prompt",
		"message": e.opts.Prompt,
	})
	if _, err := stdin.Write(append(promptFrame, '\n')); err != nil {
		_ = terminateProcessGroupAndWait(cmd, pid, stdin, 2*time.Second)
		<-stderrDone
		return fmt.Errorf("write prompt frame: %w", err)
	}

	// After agent_end fires the parser signals via agentEndCh. Pi RPC mode
	// keeps running waiting for more frames; we SIGTERM it to exit.
	agentEndCh := make(chan struct{}, 1)
	startedAt := time.Now()
	state := &translatorState{
		cwd:    e.opts.CWD,
		model:  e.opts.Model,
		taskID: e.opts.TaskID,
	}
	parseErr := streamPiEvents(ctx, stdout, stdin, onEvent, onUIRequest, e.OnToolExecutionStart, e.OnToolExecutionEnd, agentEndCh, true, state, startedAt)
	agentEnded := false
	select {
	case <-agentEndCh:
		agentEnded = true
	default:
	}
	if !agentEnded {
		waitErr := terminateProcessGroupAndWait(cmd, pid, stdin, 2*time.Second)
		<-stderrDone
		if ctx.Err() != nil {
			return nil
		}
		if parseErr != nil && parseErr != io.EOF {
			if len(stderrBuf) > 0 {
				return fmt.Errorf("%w (pi stderr: %s)", parseErr, string(stderrBuf))
			}
			return parseErr
		}
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				code := exitErr.ExitCode()
				if code > 0 {
					return piExitError(code, string(stderrBuf))
				}
			} else {
				return fmt.Errorf("pi wait: %w", waitErr)
			}
		}
		return fmt.Errorf("pi stream ended before agent_end")
	}
	waitErr := terminateProcessGroupAndWait(cmd, pid, stdin, 2*time.Second)
	<-stderrDone

	if waitErr != nil {
		if ctx.Err() != nil {
			return nil
		}
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			if code > 0 {
				return piExitError(code, string(stderrBuf))
			}
		} else {
			return fmt.Errorf("pi wait: %w", waitErr)
		}
	}
	if parseErr != nil && parseErr != io.EOF {
		if len(stderrBuf) > 0 {
			return fmt.Errorf("%w (pi stderr: %s)", parseErr, string(stderrBuf))
		}
		return parseErr
	}
	return nil
}

func browserEnv(base []string, grantID string, browserID string, sessionID string) []string {
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		if strings.HasPrefix(entry, "GSD_BROWSER_") {
			continue
		}
		env = append(env, entry)
	}
	if grantID != "" && browserID != "" && sessionID != "" {
		env = append(env,
			"GSD_BROWSER_GRANT_ID="+grantID,
			"GSD_BROWSER_ID="+browserID,
			"GSD_BROWSER_SESSION_ID="+sessionID,
		)
	}
	return env
}

func planCapabilityEnv(base []string, cap *protocol.PlanCapability) []string {
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		if strings.HasPrefix(entry, "GSD_PLAN_") {
			continue
		}
		env = append(env, entry)
	}
	if cap != nil {
		env = append(env,
			"GSD_PLAN_API_BASE_URL="+cap.APIBaseURL,
			"GSD_PLAN_CAPABILITY_TOKEN="+cap.Token,
			"GSD_PLAN_CAPABILITY_EXPIRES_AT="+cap.ExpiresAt,
		)
	}
	return env
}

func terminateProcessGroupAndWait(cmd *exec.Cmd, pid int, stdin io.Closer, timeout time.Duration) error {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if stdin != nil {
		_ = stdin.Close()
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-waitCh:
		return err
	case <-timer.C:
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return <-waitCh
	}
}

// streamPiEvents reads pi NDJSON from r, translates each event into the
// claude stream-json shape the GSD browser dispatches on, calls onEvent for
// each translated event, and on `agent_end` synthesizes a stream-json
// `result` so the actor's handleResult path can run. Signals agentEndCh
// (non-blocking) when agent_end fires so the caller can SIGTERM pi.
//
// extension_ui_request events from pi (emitted by tools that call ctx.ui.input,
// e.g. ask_human) are intercepted and routed to onUIRequest if non-nil. The
// returned answer is written back to stdin as extension_ui_response so pi
// resumes the tool. While the handler runs, the parser is blocked, which is
// fine because pi is also blocked waiting on the answer.
//
// translate=false bypasses the translator and forwards raw pi events.
func streamPiEvents(
	ctx context.Context,
	r io.Reader,
	stdin io.Writer,
	onEvent func(claude.Event) error,
	onUIRequest UIRequestHandler,
	onToolExecutionStart func(ToolExecutionStart),
	onToolExecutionEnd func(ToolExecutionEnd),
	agentEndCh chan<- struct{},
	translate bool,
	state *translatorState,
	startedAt time.Time,
) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	if state == nil {
		state = &translatorState{}
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)

		var peek struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			continue
		}

		// Pi RPC also emits {type:"response",command:"prompt",success:true};
		// skip those; they're command acks, not stream events.
		if peek.Type == "response" {
			continue
		}

		notifyToolExecutionStart(raw, onToolExecutionStart)
		notifyToolExecutionEnd(raw, onToolExecutionEnd)

		// Intercept extension_ui_request before translation; never forwarded.
		if peek.Type == "extension_ui_request" {
			if err := handleUIRequest(ctx, raw, stdin, onUIRequest); err != nil {
				return err
			}
			continue
		}

		if translate {
			for _, ev := range translatePiEvent(raw, state) {
				if err := onEvent(claude.Event{Type: ev.Type, Raw: ev.Raw}); err != nil {
					return err
				}
			}
		} else {
			if err := onEvent(claude.Event{Type: peek.Type, Raw: raw}); err != nil {
				return err
			}
		}

		// On agent_end synthesize a stream-json result event so handleResult fires,
		// then signal the caller so it can shut pi down.
		if peek.Type == "agent_end" {
			durationMs := int(time.Since(startedAt).Milliseconds())
			synth, err := synthesizeResultEvent(raw, state.sessionID, durationMs)
			if err != nil {
				return fmt.Errorf("synthesize result: %w", err)
			}
			if err := onEvent(claude.Event{Type: "result", Raw: synth}); err != nil {
				return err
			}
			select {
			case agentEndCh <- struct{}{}:
			default:
			}
			return nil
		}
	}
	return scanner.Err()
}

type piToolExecutionStart struct {
	Type            string         `json:"type"`
	ToolCallID      string         `json:"tool_call_id"`
	ToolCallIDCamel string         `json:"toolCallId"`
	ToolName        string         `json:"tool_name"`
	ToolNameCamel   string         `json:"toolName"`
	Args            map[string]any `json:"args"`
}

func notifyToolExecutionStart(raw json.RawMessage, notify func(ToolExecutionStart)) {
	if notify == nil {
		return
	}
	var event piToolExecutionStart
	if err := json.Unmarshal(raw, &event); err != nil || event.Type != "tool_execution_start" {
		return
	}
	toolCallID := event.ToolCallID
	if toolCallID == "" {
		toolCallID = event.ToolCallIDCamel
	}
	toolName := event.ToolName
	if toolName == "" {
		toolName = event.ToolNameCamel
	}
	notify(ToolExecutionStart{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Args:       event.Args,
	})
}

// ToolExecutionEnd describes a pi tool execution that has completed.
// Result is the raw pi result map (with content[] and optional details).
// IsError is true when pi reported the tool errored.
type ToolExecutionEnd struct {
	ToolCallID string
	ToolName   string
	Result     map[string]any
	IsError    bool
}

type piToolExecutionEnd struct {
	Type            string         `json:"type"`
	ToolCallID      string         `json:"tool_call_id"`
	ToolCallIDCamel string         `json:"toolCallId"`
	ToolName        string         `json:"tool_name"`
	ToolNameCamel   string         `json:"toolName"`
	Result          map[string]any `json:"result"`
	IsError         bool           `json:"isError"`
}

func notifyToolExecutionEnd(raw json.RawMessage, notify func(ToolExecutionEnd)) {
	if notify == nil {
		return
	}
	var event piToolExecutionEnd
	if err := json.Unmarshal(raw, &event); err != nil || event.Type != "tool_execution_end" {
		return
	}
	toolCallID := event.ToolCallID
	if toolCallID == "" {
		toolCallID = event.ToolCallIDCamel
	}
	toolName := event.ToolName
	if toolName == "" {
		toolName = event.ToolNameCamel
	}
	notify(ToolExecutionEnd{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Result:     event.Result,
		IsError:    event.IsError,
	})
}

// StreamFromReaderForTest drives the pi NDJSON parsing path from a synthetic
// reader. It runs the same scanner loop as Run, firing OnToolExecutionStart /
// OnToolExecutionEnd callbacks and emitting translated claude events. Intended
// for cross-package integration tests that need to exercise the full
// daemon emit path (executor → actor → relay) without spawning pi.
func (e *Executor) StreamFromReaderForTest(
	ctx context.Context,
	r io.Reader,
	onEvent func(claude.Event) error,
	onUIRequest UIRequestHandler,
) error {
	state := &translatorState{}
	agentEndCh := make(chan struct{}, 1)
	return streamPiEvents(
		ctx,
		r,
		io.Discard,
		onEvent,
		onUIRequest,
		e.OnToolExecutionStart,
		e.OnToolExecutionEnd,
		agentEndCh,
		true,
		state,
		time.Now(),
	)
}

func (e *Executor) handlePiEventForTest(ctx context.Context, raw json.RawMessage, onEvent func(claude.Event) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	notifyToolExecutionStart(raw, e.OnToolExecutionStart)
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return err
	}
	for _, ev := range translatePiEvent(raw, &translatorState{}) {
		if err := onEvent(claude.Event{Type: ev.Type, Raw: ev.Raw}); err != nil {
			return err
		}
	}
	return nil
}

// handleUIRequest routes a pi extension_ui_request to the caller's handler
// and writes the resulting extension_ui_response back to pi's stdin. If no
// handler is registered, sends a "cancelled" response so the tool returns
// cleanly rather than hanging the agent forever.
func handleUIRequest(ctx context.Context, raw json.RawMessage, stdin io.Writer, handler UIRequestHandler) error {
	var req struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		Method      string `json:"method"`
		Title       string `json:"title"`
		Placeholder string `json:"placeholder"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("decode extension_ui_request: %w", err)
	}

	// The daemon question protocol accepts text input for this tool path.
	// Unsupported UI request methods receive a cancelled response.
	if handler == nil || req.Method != "input" {
		return writeUIResponse(stdin, req.ID, true, "")
	}

	type uiResult struct {
		answer string
		err    error
	}
	resultCh := make(chan uiResult, 1)
	go func() {
		answer, err := handler(ctx, UIRequest{
			ID:          req.ID,
			Method:      req.Method,
			Title:       req.Title,
			Placeholder: req.Placeholder,
		})
		resultCh <- uiResult{answer: answer, err: err}
	}()

	var result uiResult
	select {
	case <-ctx.Done():
		_ = writeUIResponse(stdin, req.ID, true, "")
		return ctx.Err()
	case result = <-resultCh:
	}

	if result.err != nil {
		// Treat handler errors as cancellation rather than killing pi;
		// the tool will see the cancelled flag and return an error result.
		_ = writeUIResponse(stdin, req.ID, true, "")
		return fmt.Errorf("ui handler: %w", result.err)
	}
	if result.answer == "" {
		return writeUIResponse(stdin, req.ID, true, "")
	}
	return writeUIResponse(stdin, req.ID, false, result.answer)
}

func writeUIResponse(stdin io.Writer, id string, cancelled bool, value string) error {
	resp := map[string]any{
		"type": "extension_ui_response",
		"id":   id,
	}
	if cancelled {
		resp["cancelled"] = true
	} else {
		resp["value"] = value
	}
	frame, _ := json.Marshal(resp)
	_, err := stdin.Write(append(frame, '\n'))
	return err
}

// synthesizeResultEvent turns pi's agent_end into the stream-json result
// event shape that internal/session/actor.go's handleResult expects.
func synthesizeResultEvent(agentEndRaw json.RawMessage, sessionID string, durationMs int) (json.RawMessage, error) {
	var ae struct {
		Type     string `json:"type"`
		Messages []struct {
			Role  string `json:"role"`
			Usage struct {
				Input      int `json:"input"`
				Output     int `json:"output"`
				CacheRead  int `json:"cacheRead"`
				CacheWrite int `json:"cacheWrite"`
				Cost       struct {
					Total float64 `json:"total"`
				} `json:"cost"`
			} `json:"usage"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(agentEndRaw, &ae); err != nil {
		return nil, fmt.Errorf("parse agent_end: %w", err)
	}

	var totalIn, totalOut, totalCacheRead, totalCacheWrite int
	var totalCost float64
	for _, m := range ae.Messages {
		if m.Role != "assistant" {
			continue
		}
		totalIn += m.Usage.Input
		totalOut += m.Usage.Output
		totalCacheRead += m.Usage.CacheRead
		totalCacheWrite += m.Usage.CacheWrite
		totalCost += m.Usage.Cost.Total
	}

	out := map[string]any{
		"type":           "result",
		"session_id":     sessionID,
		"total_cost_usd": totalCost,
		"duration_ms":    durationMs,
		"usage": map[string]any{
			"input_tokens":                totalIn,
			"output_tokens":               totalOut,
			"cache_read_input_tokens":     totalCacheRead,
			"cache_creation_input_tokens": totalCacheWrite,
		},
	}
	return json.Marshal(out)
}
