//go:build !windows

package claude

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
)

// Options configures a Claude process.
type Options struct {
	BinaryPath     string // defaults to "claude"
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
	ptmx    *os.File // master end of the pty
	started bool
	done    chan error
	ready   chan struct{} // closed once stdin is available

	// closed by Close to tell Start's error handling that a signaled
	// exit of the subprocess is expected (we asked for it) and should
	// be swallowed. Equivalent to ctx.Err() != nil but driven by our
	// own shutdown path rather than the caller's context cancellation.
	shuttingDown atomic.Bool
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
//
// Stdin and stdout are routed through a POSIX pseudo-terminal. See
// pty_unix.go's openClaudePTY for the full rationale; the short version
// is that Node.js block-buffers pipe stdout but line-buffers TTY stdout,
// and the claude CLI is a Node program, so a pty is required for the
// daemon to see stream-json events in real time. Stderr remains a
// regular pipe so the bounded ring buffer in stderr_buffer.go can
// capture diagnostics without intermixing them with stream events.
func (e *Executor) Start(ctx context.Context, onEvent func(Event) error) error {
	// One diagnostic line at the very top of Start so it is visible in
	// fly logs / stdout whether Start was ever called for a given
	// session, independent of whether any subsequent step succeeds.
	// Without this it is impossible to distinguish "Run goroutine died
	// before Start" from "Start was entered and failed deep inside"
	// when triaging a hung session.
	log.Printf("[executor] starting claude: binary=%s dir=%s model=%q", e.opts.BinaryPath, e.opts.CWD, e.opts.Model)

	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return fmt.Errorf("executor already started")
	}
	e.started = true

	// Guarantee that e.ready is closed before Start returns, regardless
	// of which error path setup takes. Send() blocks on <-e.ready; if
	// setup fails before the success-path close below and ready is
	// never closed, any concurrent Send call hangs forever. The
	// downstream v0.1.3 smoke test exhibited exactly this: SendTask's
	// call to Send blocked indefinitely because openClaudePTY (or some
	// call under it) returned an error and the goroutine running
	// Executor.Start discarded it without ever closing ready.
	//
	// The deferred close is guarded by readyClosed so the success path,
	// which closes ready explicitly inside Start (so Send unblocks
	// while we are still inside Start, before the long parseLoop
	// below), does not double-close.
	readyClosed := false
	defer func() {
		if !readyClosed {
			close(e.ready)
		}
	}()

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

	// Allocate a pseudo-terminal for the subprocess's stdin and stdout.
	// The master (ptmx) stays with the parent; the slave (tty) gets
	// wired into the child process and must be closed by the parent
	// immediately after Start so that only the child holds it open.
	// When the child exits, the last ref on the slave drops and reads
	// on the master return EOF naturally.
	ptmx, tty, err := openClaudePTY()
	if err != nil {
		e.mu.Unlock()
		return err
	}
	cmd.Stdin = tty
	cmd.Stdout = tty
	// Attach the child to a dedicated session/process group so it
	// becomes the controlling process for this pty. Without this,
	// Node's isatty checks on stdout still return true on most
	// systems, but setsid makes the behavior explicit and matches
	// what pty.Start would have done.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = ptySysProcAttr()
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		e.mu.Unlock()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	// Capture the tail of claude's stderr into a bounded ring buffer.
	// This both prevents the stderr pipe from blocking claude's writes
	// AND makes crashes, auth errors, and rate-limit errors diagnosable
	// from the error returned by Start.
	stderrBuf := newStderrBuffer(50, 16*1024)

	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		e.mu.Unlock()
		return fmt.Errorf("start: %w", err)
	}
	// The child has inherited the slave fd via fork/exec. Drop the
	// parent's ref so only the child holds it; otherwise reads on the
	// master will never see EOF when the child exits.
	_ = tty.Close()

	e.cmd = cmd
	e.stdin = ptmx  // writes here become child stdin
	e.stdout = ptmx // reads here are child stdout
	e.ptmx = ptmx
	close(e.ready)
	readyClosed = true
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

	parseErr := Parse(ptmx, onEvent)
	waitErr := cmd.Wait()
	<-stderrDone
	_ = ptmx.Close()

	if waitErr != nil {
		// Expected shutdown paths, swallow silently:
		//  - caller canceled ctx
		//  - Close() was invoked (we SIGTERM'd the child ourselves)
		if ctx.Err() != nil || e.shuttingDown.Load() {
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

// Close signals Claude to exit. On the pipe-based implementation this
// used to close stdin; on the pty-based implementation stdin and stdout
// are the same file descriptor (the ptmx master), so closing it would
// race with the parser's read loop and drop buffered output. Instead
// we SIGTERM the child: the kernel drains any already-written bytes
// through the pty buffer to the parent's read side, then delivers EOF
// to the parser. cmd.Wait returns with a signaled exit, which Start's
// error handling recognises as an expected shutdown via shuttingDown.
func (e *Executor) Close() error {
	e.mu.Lock()
	cmd := e.cmd
	e.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	e.shuttingDown.Store(true)
	// SIGTERM is graceful; the child gets a chance to finalize any
	// in-flight writes before the kernel tears down the pty. On Node
	// this is sufficient — the claude CLI exits within a few ms.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	return nil
}
