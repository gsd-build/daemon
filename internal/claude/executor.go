//go:build !windows

package claude

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
)

// Options configures a Claude process.
type Options struct {
	BinaryPath     string
	CWD            string
	Model          string
	Effort         string
	PermissionMode string
	SystemPrompt   string
	ResumeSession  string   // claude session id to resume; empty = new session
	AllowedTools   []string // tools to pass via --allowedTools
	Env            []string // extra environment variables; nil = inherit
	Prompt         string   // user's message text
}

// Executor spawns a single `claude -p` process and reads its output.
type Executor struct {
	opts Options
}

// NewExecutor constructs an Executor. Call Run to spawn the process.
func NewExecutor(opts Options) *Executor {
	if opts.BinaryPath == "" {
		opts.BinaryPath = "claude"
	}
	return &Executor{opts: opts}
}

// Run spawns the claude process with the prompt as a CLI argument,
// parses stream-json events from stdout, and blocks until the process
// exits or ctx is canceled. Each event is delivered via onEvent.
//
// Stdout is a regular pipe. Claude CLI (Node.js) block-buffers the
// first ~4KB then flushes per-line. The initial batch delay is
// imperceptible against Claude's multi-second response time.
//
// Stderr is captured in a bounded ring buffer so crashes, auth errors,
// and rate-limit messages surface in the returned error.
func (e *Executor) Run(ctx context.Context, onEvent func(Event) error) error {
	log.Printf("[executor] starting claude: binary=%s dir=%s model=%q prompt=%q",
		e.opts.BinaryPath, e.opts.CWD, e.opts.Model, truncateStr(e.opts.Prompt, 80))

	args := []string{
		"-p",
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
	// Prompt as positional argument — must be last
	args = append(args, e.opts.Prompt)

	cmd := exec.CommandContext(ctx, e.opts.BinaryPath, args...)
	cmd.Dir = e.opts.CWD
	if len(e.opts.Env) > 0 {
		cmd.Env = append(os.Environ(), e.opts.Env...)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	stderrBuf := newStderrBuffer(50, 16*1024)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Drain stderr in background
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		stderrBuf.drain(stderr)
	}()

	parseErr := Parse(stdout, onEvent)
	waitErr := cmd.Wait()
	<-stderrDone

	if waitErr != nil {
		if ctx.Err() != nil {
			return nil // expected shutdown via context cancellation
		}
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			if code > 0 {
				if tail := stderrBuf.String(); tail != "" {
					return fmt.Errorf("claude exited with code %d: %s", code, tail)
				}
				return fmt.Errorf("claude exited with code %d (no stderr)", code)
			}
		} else {
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

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
