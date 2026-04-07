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
	cmd.Stderr = nil // ignore stderr to avoid pipe deadlocks

	if err := cmd.Start(); err != nil {
		e.mu.Unlock()
		return fmt.Errorf("start: %w", err)
	}
	e.cmd = cmd
	e.stdin = stdin
	e.stdout = stdout
	close(e.ready)
	e.mu.Unlock()

	parseErr := Parse(stdout, onEvent)
	waitErr := cmd.Wait()

	if parseErr != nil && parseErr != io.EOF {
		return parseErr
	}
	if waitErr != nil {
		// Non-zero exit is expected when we close stdin
		return nil
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
