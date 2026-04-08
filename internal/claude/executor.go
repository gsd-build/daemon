package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Options configures a Claude process.
type Options struct {
	BinaryPath     string   // defaults to "claude"
	CWD            string
	Model          string
	Effort         string
	PermissionMode string
	SystemPrompt   string
	ResumeSession  string   // claude session id to resume; empty = new session
	AllowedTools   []string // tools to pass via --allowedTools; accumulates as user grants
	Env            []string // extra environment variables (e.g. for tests); nil = inherit
}

// Executor owns a single `claude -p` subprocess.
type Executor struct {
	opts Options

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	started bool
	done    chan error
	ready   chan struct{} // closed once stdin is available
}

// NewExecutor constructs an Executor but does not start the process.
func NewExecutor(opts Options) *Executor {
	if opts.BinaryPath == "" {
		opts.BinaryPath = "claude"
	}
	return &Executor{
		opts:  opts,
		done:  make(chan error, 1),
		ready: make(chan struct{}),
	}
}

// Start spawns the process and begins parsing events. It blocks until
// the process exits or ctx is canceled. Events are delivered via onEvent.
func (e *Executor) Start(ctx context.Context, onEvent func(Event) error) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return fmt.Errorf("executor already started")
	}
	e.started = true

	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	if e.opts.Model != "" {
		args = append(args, "--model", e.opts.Model)
	}
	if e.opts.Effort != "" {
		args = append(args, "--effort", e.opts.Effort)
	}
	if e.opts.PermissionMode != "" {
		args = append(args, "--permission-mode", e.opts.PermissionMode)
	}
	if e.opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", e.opts.SystemPrompt)
	}
	if e.opts.ResumeSession != "" {
		args = append(args, "--resume", e.opts.ResumeSession)
	}
	for _, tool := range e.opts.AllowedTools {
		args = append(args, "--allowedTools", tool)
	}

	cmd := exec.CommandContext(ctx, e.opts.BinaryPath, args...)
	cmd.Dir = e.opts.CWD
	if len(e.opts.Env) > 0 {
		cmd.Env = append(os.Environ(), e.opts.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	// Capture the tail of claude's stderr into a bounded ring buffer.
	// This both prevents the stderr pipe from blocking claude's writes
	// (the original reason the field was set to nil) AND makes crashes,
	// auth errors, and rate-limit errors diagnosable from the error
	// returned by Start.
	stderrBuf := newStderrBuffer(50, 16*1024)

	if err := cmd.Start(); err != nil {
		e.mu.Unlock()
		return fmt.Errorf("start: %w", err)
	}
	e.cmd = cmd
	e.stdin = stdin
	e.stdout = stdout
	close(e.ready)
	e.mu.Unlock()

	// Drain stderr in a goroutine. It exits on its own when claude
	// closes stderr (naturally on process exit). We join it below after
	// cmd.Wait so the ring buffer is fully populated before we format
	// any error message from it.
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		stderrBuf.drain(stderr)
	}()

	parseErr := Parse(stdout, onEvent)
	waitErr := cmd.Wait()
	<-stderrDone

	if waitErr != nil {
		// If the caller canceled ctx (normal shutdown path, including
		// RestartWithGrant closing stdin via Close()), swallow silently.
		if ctx.Err() != nil {
			return nil
		}
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			if code > 0 {
				if tail := stderrBuf.String(); tail != "" {
					return fmt.Errorf("claude exited with code %d: %s", code, tail)
				}
				return fmt.Errorf("claude exited with code %d (no stderr)", code)
			}
			// ExitCode() == -1 indicates signaled exit. Our shutdown
			// path closes stdin, so claude normally exits 0; a signaled
			// exit here without ctx.Err() set is unexpected but not
			// actionable from stderr alone — propagate the raw error
			// below via the parseErr branch if one exists, otherwise
			// fall through and return nil.
		} else {
			// Non-ExitError (pipe i/o error, etc.) — propagate.
			return fmt.Errorf("claude wait: %w", waitErr)
		}
	}
	if parseErr != nil && parseErr != io.EOF {
		if tail := stderrBuf.String(); tail != "" {
			return fmt.Errorf("%w (claude stderr: %s)", parseErr, tail)
		}
		return parseErr
	}
	return nil
}

// Send writes a user message to the process stdin as NDJSON.
// It blocks until the process is ready (stdin pipe is open).
func (e *Executor) Send(text string) error {
	<-e.ready

	e.mu.Lock()
	stdin := e.stdin
	e.mu.Unlock()
	if stdin == nil {
		return fmt.Errorf("not started")
	}

	msg := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q}}`+"\n", text)
	_, err := stdin.Write([]byte(msg))
	return err
}

// Close closes stdin, signaling Claude to exit.
func (e *Executor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stdin == nil {
		return nil
	}
	return e.stdin.Close()
}
