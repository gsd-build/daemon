//go:build !windows

package terminal

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	events   EventSender
	limits   Limits
}

type Session struct {
	req       OpenRequest
	cmd       *exec.Cmd
	ptmx      *os.File
	shell     string
	cwd       string
	ring      *ScrollbackRing
	seq       int64
	done      chan struct{}
	closeOnce sync.Once
}

func NewManager(events EventSender, limits Limits) *Manager {
	if limits.ScrollbackBytes == 0 {
		limits = DefaultLimits()
	}
	return &Manager{
		sessions: make(map[string]*Session),
		events:   events,
		limits:   limits,
	}
}

func (m *Manager) Open(ctx context.Context, req OpenRequest) error {
	cwd, err := ValidateCWD(req.CWD)
	if err != nil {
		_ = m.events.SendTerminalError(req.RequestID, req.TerminalID, req.SessionID, req.ChannelID, err.Error())
		return err
	}
	shell := ResolveShell()
	cmd := exec.CommandContext(ctx, shell)
	cmd.Dir = cwd
	cmd.Env = sanitizedEnv()
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(clamp(req.Cols, 20, 300)), Rows: uint16(clamp(req.Rows, 5, 100))})
	if err != nil {
		_ = m.events.SendTerminalError(req.RequestID, req.TerminalID, req.SessionID, req.ChannelID, "Unable to start shell")
		return fmt.Errorf("start pty: %w", err)
	}
	s := &Session{
		req:   req,
		cmd:   cmd,
		ptmx:  ptmx,
		shell: shell,
		cwd:   cwd,
		ring:  NewScrollbackRing(m.limits.ScrollbackBytes),
		done:  make(chan struct{}),
	}
	m.mu.Lock()
	if existing := m.sessions[req.TerminalID]; existing != nil {
		m.mu.Unlock()
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		return fmt.Errorf("terminal already exists")
	}
	m.sessions[req.TerminalID] = s
	m.mu.Unlock()
	_ = m.events.SendTerminalOpened(req, shell, cwd, time.Now().UTC())
	go m.readLoop(s)
	go m.waitLoop(s)
	return nil
}

func (m *Manager) Input(terminalID string, data []byte) error {
	s, ok := m.get(terminalID)
	if !ok {
		return fmt.Errorf("terminal not found")
	}
	_, err := s.ptmx.Write(data)
	return err
}

func (m *Manager) Resize(terminalID string, cols, rows int) error {
	s, ok := m.get(terminalID)
	if !ok {
		return fmt.Errorf("terminal not found")
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(clamp(cols, 20, 300)), Rows: uint16(clamp(rows, 5, 100))})
}

func (m *Manager) Snapshot(terminalID string) error {
	s, ok := m.get(terminalID)
	if !ok {
		return fmt.Errorf("terminal not found")
	}
	return m.events.SendTerminalSnapshot(s.req.TerminalID, s.req.SessionID, s.req.ChannelID, s.ring.Seq(), s.ring.Bytes())
}

func (m *Manager) Close(terminalID string, reason string) {
	s, ok := m.get(terminalID)
	if !ok {
		return
	}
	s.closeOnce.Do(func() {
		if s.cmd.Process != nil {
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
			time.AfterFunc(2*time.Second, func() {
				select {
				case <-s.done:
				default:
					_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
				}
			})
		}
	})
}

func (m *Manager) readLoop(s *Session) {
	buf := make([]byte, m.limits.OutputChunkSize)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.seq++
			chunk := append([]byte(nil), buf[:n]...)
			s.ring.WriteChunk(s.seq, chunk)
			_ = m.events.SendTerminalOutput(s.req.TerminalID, s.req.SessionID, s.req.ChannelID, s.seq, chunk)
		}
		if err != nil {
			if err != io.EOF {
				_ = m.events.SendTerminalError(s.req.RequestID, s.req.TerminalID, s.req.SessionID, s.req.ChannelID, "Terminal stream ended")
			}
			return
		}
	}
}

func (m *Manager) waitLoop(s *Session) {
	err := s.cmd.Wait()
	close(s.done)
	_ = s.ptmx.Close()
	m.mu.Lock()
	delete(m.sessions, s.req.TerminalID)
	m.mu.Unlock()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	_ = m.events.SendTerminalExit(s.req.TerminalID, s.req.SessionID, s.req.ChannelID, ReasonProcessExit, exitCode, "", time.Now().UTC())
}

func (m *Manager) get(terminalID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[terminalID]
	return s, ok
}

func sanitizedEnv() []string {
	env := os.Environ()
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	return env
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
