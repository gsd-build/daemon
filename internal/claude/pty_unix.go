//go:build !windows

package claude

import (
	"fmt"
	"os"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// ptySysProcAttr returns the platform-specific SysProcAttr needed for a
// child process whose stdin/stdout are wired to a pty slave. Setsid
// creates a new session and detaches the child from any controlling
// terminal the parent had, and Setctty + Ctty=0 make the slave fd
// (fd 0 in the child after it inherits stdin) the child's new
// controlling terminal. Without this, Node's isatty checks may still
// succeed on most kernels, but behavior is implementation-defined;
// setting it explicitly is what creack/pty's pty.Start does
// internally and matches the standard PTY pattern.
func ptySysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
}

// openClaudePTY allocates a pseudo-terminal pair suitable for hosting a
// Claude Code CLI subprocess. The returned ptmx is the master end: the
// parent reads claude's stdout and writes stdin through it. The returned
// tty is the slave end: the caller must set cmd.Stdin and cmd.Stdout to
// it BEFORE calling cmd.Start, and close it in the parent immediately
// AFTER cmd.Start so only the child holds the slave open.
//
// Why a PTY and not a pipe: Node.js (the runtime the claude CLI is built
// on) detects whether process.stdout is a TTY. On a pipe, Node switches to
// block-buffered stdout — it holds emitted bytes in an internal 4–8 KiB
// userspace buffer until the buffer fills or the process exits. Small
// stream-json events (a typical "hello" response is ~200 bytes) never
// fill the buffer and therefore never reach the daemon until the CLI is
// closed. On a TTY, Node uses line buffering instead and flushes on every
// newline, which matches exactly what the daemon's line-based parser
// expects. Allocating a PTY sidesteps the problem entirely from the
// Go side, without requiring any changes to the claude CLI or a coreutils
// dependency like stdbuf.
//
// The PTY is configured immediately after allocation:
//
//  1. A huge window size (10000×10000) is set so Node does not wrap
//     stream-json events at terminal-width boundaries. Cost is zero (four
//     integers in an ioctl); benefit is that arbitrarily long events
//     (large tool outputs, long assistant responses, multi-KB diffs)
//     pass through unwrapped.
//
//  2. The master side is placed in raw mode via term.MakeRaw. This
//     disables input echo (so the JSON messages we write to the master
//     are not reflected back to us on the master's read side),
//     canonical mode (so input is not line-buffered inside the kernel
//     before reaching the child), and output post-processing (so LF
//     output from the child is not translated to CRLF). Without raw
//     mode, every byte we write as stdin would appear interleaved in
//     stdout and the parser would fail on malformed JSON.
func openClaudePTY() (ptmx, tty *os.File, err error) {
	ptmx, tty, err = pty.Open()
	if err != nil {
		return nil, nil, fmt.Errorf("pty open: %w", err)
	}

	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 10000, Cols: 10000}); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		return nil, nil, fmt.Errorf("pty setsize: %w", err)
	}

	// Put the master in raw mode. This disables ECHO/ICANON/OPOST on
	// the line discipline associated with this pty pair. We discard
	// the previous termios state because the pty is always torn down
	// (ptmx.Close) when the child exits — there is nothing to restore.
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		return nil, nil, fmt.Errorf("pty make raw: %w", err)
	}

	return ptmx, tty, nil
}
