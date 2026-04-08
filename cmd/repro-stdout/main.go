// repro-stdout is a minimal standalone reproduction harness for the
// "claude subprocess stdout silent" bug that motivated the pty path in
// internal/claude/executor.go. It spawns a claude binary (real or a
// compatible fake), feeds it one stream-json user message on stdin,
// reads stdout for up to 10 seconds, and prints how many events it
// received.
//
// Usage:
//
//	go run ./cmd/repro-stdout --mode pipe  --bin claude
//	go run ./cmd/repro-stdout --mode pty   --bin claude
//	go run ./cmd/repro-stdout --mode pipe  --bin ./fake-claude-blockbuf
//	go run ./cmd/repro-stdout --mode pty   --bin ./fake-claude-blockbuf
//
// --mode pipe sets up the child's stdin/stdout as anonymous pipes,
// which is the pre-fix pipe-based executor path. --mode pty allocates
// a pseudo-terminal and wires the child's stdin+stdout to the slave,
// which is the fix. Against real claude or fake-claude-blockbuf:
//
//	--mode pipe  → 0 events read (block-buffered stdout, never flushed)
//	--mode pty   → 3+ events read (line-buffered, flushed per event)
//
// Exit code: 0 if N>0 events were read within the timeout, 1 otherwise.
// This makes the binary usable as a smoke-test in CI:
//
//	repro-stdout --mode pty --bin $(which claude) || exit 1
//
// This binary is committed permanently as living documentation of the
// bug AND as a regression reference for anyone debugging a similar
// stdio interaction with a Node-based subprocess in the future. Do not
// delete it.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

const userMessage = `{"type":"user","message":{"role":"user","content":"say hello in three words"}}` + "\n"

func main() {
	var (
		mode    = flag.String("mode", "pty", "stdio mode: pipe or pty")
		binary  = flag.String("bin", "claude", "path to the claude binary to exercise")
		timeout = flag.Duration("timeout", 10*time.Second, "how long to wait for stdout events")
	)
	flag.Parse()

	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, *binary, args...)

	var (
		stdin  io.WriteCloser
		stdout io.ReadCloser
		ptmx   *os.File
		tty    *os.File
	)

	switch *mode {
	case "pipe":
		var err error
		stdin, err = cmd.StdinPipe()
		if err != nil {
			fatal("stdin pipe: %v", err)
		}
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			fatal("stdout pipe: %v", err)
		}
		cmd.Stderr = os.Stderr
	case "pty":
		var err error
		ptmx, tty, err = pty.Open()
		if err != nil {
			fatal("pty open: %v", err)
		}
		defer ptmx.Close()
		if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 10000, Cols: 10000}); err != nil {
			fatal("pty setsize: %v", err)
		}
		if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
			fatal("pty make raw: %v", err)
		}
		cmd.Stdin = tty
		cmd.Stdout = tty
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setsid:  true,
			Setctty: true,
			Ctty:    0,
		}
		stdin = ptmx
		stdout = ptmx
	default:
		fatal("unknown --mode %q (expected pipe or pty)", *mode)
	}

	if err := cmd.Start(); err != nil {
		fatal("start: %v", err)
	}
	if *mode == "pty" {
		// Drop the parent's reference to the slave so only the child
		// holds it; otherwise reads on the master will not EOF when
		// the child exits.
		_ = tty.Close()
	}

	if _, err := stdin.Write([]byte(userMessage)); err != nil {
		fatal("write stdin: %v", err)
	}

	// Read events. The scanner reads lines from stdout into a shared
	// result struct protected by a mutex. The main goroutine polls the
	// shared state until either the expected number of events (3) has
	// arrived or the timeout fires. On a pipe-based executor with a
	// block-buffering child (real claude or fake-claude-blockbuf),
	// scanner.Scan never returns any bytes until EOF, so the poll
	// reliably sees eventCount stay at 0 until the timeout.
	type result struct {
		eventCount int
		firstThree []string
	}
	var (
		mu  sync.Mutex
		res result
	)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if len(line) == 0 {
				continue
			}
			var peek struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &peek); err != nil {
				continue
			}
			mu.Lock()
			res.eventCount++
			if len(res.firstThree) < 3 {
				res.firstThree = append(res.firstThree, peek.Type)
			}
			mu.Unlock()
		}
	}()

	const wantEvents = 3
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := res.eventCount
		mu.Unlock()
		if got >= wantEvents {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Always tear the child down so cmd.Wait returns.
	_ = cmd.Process.Signal(syscall.SIGTERM)

	mu.Lock()
	snapshot := res
	mu.Unlock()
	fmt.Printf("mode=%s bin=%s events=%d firstThree=%v\n", *mode, *binary, snapshot.eventCount, snapshot.firstThree)
	_ = cmd.Wait()
	if snapshot.eventCount == 0 {
		os.Exit(1)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "repro-stdout: "+format+"\n", args...)
	os.Exit(2)
}
