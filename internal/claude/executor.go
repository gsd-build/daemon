//go:build !windows

package claude

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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
	ImageURLs      []string // user-attached image URLs; prepended as markdown image refs
}

// Executor spawns a single `claude -p` process and reads its output.
type Executor struct {
	opts Options

	// OnPIDStart is called with the child PID after a successful Start().
	// Set by the actor for PID file tracking. May be nil.
	OnPIDStart func(pid int)

	// OnPIDExit is called with the child PID after the process exits.
	// Set by the actor for PID file cleanup. May be nil.
	OnPIDExit func(pid int)
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
	slog.Info("starting claude", "binary", e.opts.BinaryPath, "dir", e.opts.CWD, "model", e.opts.Model, "promptLen", len(e.opts.Prompt))
	slog.Debug("claude prompt", "prompt", truncateStr(e.opts.Prompt, 200))

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
	if len(e.opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(e.opts.AllowedTools, ","))
	}
	// Download user-attached images to temp files so Claude can read them
	prompt := e.opts.Prompt
	if len(e.opts.ImageURLs) > 0 {
		var imgPaths []string
		for i, u := range e.opts.ImageURLs {
			tmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("gsd-upload-%d-%d.png", time.Now().UnixMilli(), i))
			if err := downloadFile(u, tmpPath); err != nil {
				slog.Warn("failed to download user image", "url", u, "err", err)
				continue
			}
			imgPaths = append(imgPaths, tmpPath)
		}
		if len(imgPaths) > 0 {
			var prefix strings.Builder
			prefix.WriteString("The user attached the following image(s). Read each file to see them:\n")
			for _, p := range imgPaths {
				fmt.Fprintf(&prefix, "- %s\n", p)
			}
			prefix.WriteString("\n")
			prompt = prefix.String() + prompt
		}
	}
	// "--" stops flag parsing so the prompt is never consumed by variadic flags
	args = append(args, "--", prompt)

	cmd := exec.CommandContext(ctx, e.opts.BinaryPath, args...)
	cmd.Dir = e.opts.CWD
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
		if ctx.Err() != nil {
			return nil // context was cancelled before or during start
		}
		return fmt.Errorf("start: %w", err)
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

// downloadFile fetches a URL and writes it to dst.
func downloadFile(url, dst string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
