package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
)

type PromptRequest struct {
	TaskID               string
	Message              string
	OnEvent              func(claude.Event) error
	OnUIRequest          UIRequestHandler
	OnToolExecutionStart func(ToolExecutionStart)
	OnToolExecutionEnd   func(ToolExecutionEnd)
}

type Worker struct {
	opts Options
	key  WorkerKey

	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.Reader
	stderrBuf  []byte
	stderrDone chan struct{}
	pid        int
	startedAt  time.Time
	lastUsedAt time.Time
	idleSince  *time.Time
	broken     bool
	running    bool

	OnPIDStart func(pid int)
	OnPIDExit  func(pid int)
}

func NewWorker(opts Options) *Worker {
	if opts.BinaryPath == "" {
		opts.BinaryPath = "pi"
	}
	opts.Provider = ProviderOrDefault(opts.Provider)
	return &Worker{opts: opts, key: NewWorkerKey(opts)}
}

func (w *Worker) Key() WorkerKey {
	return w.key
}

func (w *Worker) PID() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pid
}

func (w *Worker) IsIdle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cmd != nil && !w.running && !w.broken
}

func (w *Worker) Snapshot(sessionID string) WorkerSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := "stopped"
	if w.cmd != nil && w.running {
		state = "executing"
	} else if w.cmd != nil && !w.broken {
		state = "idle"
	} else if w.broken {
		state = "broken"
	}
	return WorkerSnapshot{
		SessionID:  sessionID,
		Provider:   w.opts.Provider,
		Model:      w.opts.Model,
		PID:        w.pid,
		KeyHash:    w.key.Hash(),
		State:      state,
		StartedAt:  w.startedAt,
		LastUsedAt: w.lastUsedAt,
		IdleSince:  w.idleSince,
	}
}

func (w *Worker) startLocked() error {
	if w.cmd != nil && !w.broken {
		return nil
	}
	if w.opts.ExtensionPath == "" {
		return fmt.Errorf("pi extension path is required")
	}
	if _, err := os.Stat(w.opts.ExtensionPath); err != nil {
		return fmt.Errorf("pi extension not found at %s: %w", w.opts.ExtensionPath, err)
	}

	cmd := piRPCCommand(context.Background(), w.opts.BinaryPath, w.opts.CWD, w.opts.ResumeSession, processArgs(w.opts)...)
	cmd.Env = processEnv(context.Background(), os.Environ(), w.opts)
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
		return fmt.Errorf("start pi: %w", err)
	}

	w.cmd = cmd
	w.stdin = stdin
	w.stdout = stdout
	w.pid = cmd.Process.Pid
	now := time.Now()
	w.startedAt = now
	w.lastUsedAt = now
	w.idleSince = &now
	w.broken = false
	w.stderrBuf = nil
	w.stderrDone = make(chan struct{})
	go w.drainStderr(stderr, w.stderrDone)

	if w.OnPIDStart != nil {
		w.OnPIDStart(w.pid)
	}
	slog.Info("pi_worker_start", "pid", w.pid, "provider", w.opts.Provider, "model", w.opts.Model, "key", w.key.Hash())
	return nil
}

func (w *Worker) drainStderr(stderr io.Reader, done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 4096)
	for {
		n, err := stderr.Read(buf)
		w.mu.Lock()
		if n > 0 {
			w.stderrBuf = append(w.stderrBuf, buf[:n]...)
			if len(w.stderrBuf) > 16*1024 {
				w.stderrBuf = w.stderrBuf[len(w.stderrBuf)-16*1024:]
			}
		}
		w.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (w *Worker) Prompt(ctx context.Context, req PromptRequest) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return fmt.Errorf("pi worker already has an active prompt")
	}
	if err := w.startLocked(); err != nil {
		w.mu.Unlock()
		return err
	}
	w.running = true
	w.idleSince = nil
	w.lastUsedAt = time.Now()
	stdin := w.stdin
	stdout := w.stdout
	startedAt := time.Now()
	pid := w.pid
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.running = false
		now := time.Now()
		w.lastUsedAt = now
		if !w.broken {
			w.idleSince = &now
		}
		w.mu.Unlock()
	}()

	doneWatching := make(chan struct{})
	defer close(doneWatching)
	go func() {
		select {
		case <-ctx.Done():
			w.markBroken()
			_ = syscall.Kill(-pid, syscall.SIGTERM)
		case <-doneWatching:
		}
	}()

	frame, _ := json.Marshal(map[string]any{
		"id":      "task-" + req.TaskID,
		"type":    "prompt",
		"message": req.Message,
	})
	if _, err := stdin.Write(append(frame, '\n')); err != nil {
		return w.stopAfterFailure(fmt.Errorf("write prompt frame: %w", err))
	}

	agentEndCh := make(chan struct{}, 1)
	state := &translatorState{
		cwd:    w.opts.CWD,
		model:  w.opts.Model,
		taskID: req.TaskID,
	}
	err := streamPiEvents(
		ctx,
		stdout,
		stdin,
		req.OnEvent,
		req.OnUIRequest,
		req.OnToolExecutionStart,
		req.OnToolExecutionEnd,
		nil,
		agentEndCh,
		true,
		state,
		startedAt,
	)
	if err != nil {
		return w.stopAfterFailure(err)
	}
	select {
	case <-agentEndCh:
		return nil
	default:
		if ctx.Err() != nil {
			return w.stopAfterFailure(ctx.Err())
		}
		w.mu.Lock()
		stderr := string(w.stderrBuf)
		w.mu.Unlock()
		if stderr != "" {
			return w.stopAfterFailure(fmt.Errorf("pi stream ended before agent_end (pi stderr: %s)", stderr))
		}
		return w.stopAfterFailure(fmt.Errorf("pi stream ended before agent_end"))
	}
}

func (w *Worker) markBroken() {
	w.mu.Lock()
	w.broken = true
	w.idleSince = nil
	w.mu.Unlock()
}

func (w *Worker) stopAfterFailure(err error) error {
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if stopErr := w.Stop(stopCtx); stopErr != nil {
		return fmt.Errorf("%w; stop worker after failure: %v", err, stopErr)
	}
	return err
}

func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cmd := w.cmd
	pid := w.pid
	stdin := w.stdin
	done := w.stderrDone
	w.cmd = nil
	w.stdin = nil
	w.stdout = nil
	w.pid = 0
	w.running = false
	w.broken = true
	w.idleSince = nil
	w.mu.Unlock()

	if cmd == nil {
		return nil
	}
	err := terminateProcessGroupAndWait(cmd, pid, stdin, 2*time.Second)
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if w.OnPIDExit != nil {
		w.OnPIDExit(pid)
	}
	slog.Info("pi_worker_stop", "pid", pid, "provider", w.opts.Provider, "model", w.opts.Model)
	return err
}
