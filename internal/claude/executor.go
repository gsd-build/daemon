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
// Stdio is split into three different transports:
//
//   - stdin: a regular anonymous pipe (cmd.StdinPipe). The claude CLI's
//     `-p --input-format stream-json` mode requires stdin to NOT be a
//     TTY; with a TTY stdin it silently ignores --input-format, enters
//     interactive mode, and exits with "Input must be provided either
//     through stdin or as a prompt argument when using --print" because
//     interactive -p insists on a prompt arg. A pipe sidesteps this and
//     lets the daemon inject stream-json user messages normally.
//
//   - stdout: the slave end of a POSIX pseudo-terminal (ptmx). Node.js
//     block-buffers pipe stdout but line-buffers TTY stdout; the claude
//     CLI is a Node program, so without a pty slave on stdout, short
//     stream-json events (~200 bytes) sit in Node's userspace buffer
//     and never reach the daemon until the process exits. Attaching a
//     pty slave makes Node detect a TTY and flush per line. See
//     pty_unix.go's openClaudePTY for the kernel-level details.
//
//   - stderr: a regular anonymous pipe (cmd.StderrPipe), drained into
//     a bounded ring buffer so crashes, auth errors, and rate-limit
//     errors surface in the error returned by Start instead of being
//     silently discarded.
//
// Getting stdin-is-pipe + stdout-is-tty right requires asymmetric wiring:
// we allocate a pty pair but only bind the slave to cmd.Stdout, leaving
// cmd.Stdin on StdinPipe. Node's libuv checks isTTY per fd, so it will
// correctly see stdin as a pipe and stdout as a TTY and choose the
// right buffering strategy for each.
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

	// stdin: anonymous pipe. Must be a pipe (not a TTY) so claude's
	// `-p --input-format stream-json` mode accepts the stream-json
	// payload instead of dropping into interactive mode and exiting.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		e.mu.Unlock()
		return fmt.Errorf("stdin pipe: %w", err)
	}

	// stdout: slave end of a pseudo-terminal. The master (ptmx) stays
	// with the parent; the slave (tty) is bound to cmd.Stdout and must
	// be closed by the parent immediately after Start so only the
	// child holds it open. When the child exits, the last ref on the
	// slave drops and reads on the master return EOF naturally.
	ptmx, tty, err := openClaudePTY()
	if err != nil {
		_ = stdinPipe.Close()
		e.mu.Unlock()
		return err
	}
	cmd.Stdout = tty
	// Attach the child to a dedicated session/process group so its
	// controlling terminal is the pty slave on fd 1. Without setsid
	// + setctty, Node's isTTY on stdout still reports true on most
	// kernels, but being explicit here matches what pty.Start does
	// internally and avoids any controlling-tty ambiguity.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = ptySysProcAttr()
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
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
		_ = stdinPipe.Close()
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
	e.stdin = stdinPipe // Send() writes to the child's stdin pipe
	e.stdout = ptmx     // Parse() reads from the pty master
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

// Close signals claude to exit by closing its stdin. Under the split
// stdio layout stdin is a dedicated pipe that is NOT the same fd as
// stdout (stdout is a pty master), so closing stdin here is safe: it
// only EOFs the child's input, it does not disturb the parser's
// in-flight read of the pty master. Claude sees EOF on stdin and exits
// cleanly a few milliseconds later, cmd.Wait returns with exit code 0,
// and Start's normal return path tears down the pty.
//
// shuttingDown is still set so that if claude races and exits signaled
// instead of clean (unlikely but possible), Start treats it as an
// expected shutdown rather than a subprocess crash.
func (e *Executor) Close() error {
	e.mu.Lock()
	stdin := e.stdin
	e.mu.Unlock()
	if stdin == nil {
		return nil
	}
	e.shuttingDown.Store(true)
	return stdin.Close()
}
