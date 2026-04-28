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
	req         OpenRequest
	cmd         *exec.Cmd
	ptmx        *os.File
	shell       string
	cwd         string
	ring        *ScrollbackRing
	seq         int64
	done        chan struct{}
	closeOnce   sync.Once
	reasonMu    sync.Mutex
	closeReason string
	timerMu     sync.Mutex
	idleTimer   *time.Timer
	maxTimer    *time.Timer
}

func NewManager(events EventSender, limits Limits) *Manager {
	limits = normalizeLimits(limits)
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
	m.mu.Lock()
	if existing := m.sessions[req.TerminalID]; existing != nil {
		m.mu.Unlock()
		return fmt.Errorf("terminal already exists")
	}
	if m.limits.MaxSessions > 0 && len(m.sessions) >= m.limits.MaxSessions {
		m.mu.Unlock()
		_ = m.events.SendTerminalError(req.RequestID, req.TerminalID, req.SessionID, req.ChannelID, "Terminal limit reached")
		return fmt.Errorf("terminal limit reached")
	}
	m.mu.Unlock()

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
	if m.limits.MaxSessions > 0 && len(m.sessions) >= m.limits.MaxSessions {
		m.mu.Unlock()
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = m.events.SendTerminalError(req.RequestID, req.TerminalID, req.SessionID, req.ChannelID, "Terminal limit reached")
		return fmt.Errorf("terminal limit reached")
	}
	m.sessions[req.TerminalID] = s
	m.mu.Unlock()
	_ = m.events.SendTerminalOpened(req, shell, cwd, time.Now().UTC())
	go m.readLoop(s)
	go m.waitLoop(s)
	m.startTimers(s)
	return nil
}

func (m *Manager) Input(terminalID string, data []byte) error {
	s, ok := m.get(terminalID)
	if !ok {
		return fmt.Errorf("terminal not found")
	}
	m.touch(s)
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
	m.closeSession(s, reason)
}

func (m *Manager) CloseAll(ctx context.Context, reason string) {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	for _, s := range sessions {
		m.closeSession(s, reason)
	}
	for _, s := range sessions {
		select {
		case <-s.done:
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) closeSession(s *Session, reason string) {
	s.closeOnce.Do(func() {
		s.setCloseReason(reason)
		m.stopTimers(s)
		_ = s.ptmx.Close()
		if s.cmd.Process != nil {
			if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM); err != nil {
				_ = s.cmd.Process.Signal(syscall.SIGTERM)
			}
			time.AfterFunc(m.limits.TerminationGracePeriod, func() {
				select {
				case <-s.done:
				default:
					if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL); err != nil {
						_ = s.cmd.Process.Kill()
					}
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
	_ = s.ptmx.Close()
	m.stopTimers(s)
	m.mu.Lock()
	delete(m.sessions, s.req.TerminalID)
	m.mu.Unlock()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	_ = m.events.SendTerminalExit(s.req.TerminalID, s.req.SessionID, s.req.ChannelID, s.exitReason(), exitCode, "", time.Now().UTC())
	close(s.done)
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

func (m *Manager) startTimers(s *Session) {
	s.timerMu.Lock()
	defer s.timerMu.Unlock()

	idleTimeout := s.req.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = m.limits.IdleTimeout
	}
	if idleTimeout > 0 {
		s.idleTimer = time.AfterFunc(idleTimeout, func() {
			m.closeSession(s, ReasonDisconnectTimeout)
		})
	}
	maxLifetime := s.req.MaxLifetime
	if maxLifetime <= 0 {
		maxLifetime = m.limits.MaxLifetime
	}
	if maxLifetime > 0 {
		s.maxTimer = time.AfterFunc(maxLifetime, func() {
			m.closeSession(s, ReasonMaxLifetime)
		})
	}
}

func (m *Manager) touch(s *Session) {
	idleTimeout := s.req.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = m.limits.IdleTimeout
	}
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if s.idleTimer != nil && idleTimeout > 0 {
		s.idleTimer.Reset(idleTimeout)
	}
}

func (m *Manager) stopTimers(s *Session) {
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	if s.maxTimer != nil {
		s.maxTimer.Stop()
	}
}

func (s *Session) setCloseReason(reason string) {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	s.closeReason = reason
}

func (s *Session) exitReason() string {
	s.reasonMu.Lock()
	defer s.reasonMu.Unlock()
	if s.closeReason != "" {
		return s.closeReason
	}
	return ReasonProcessExit
}

func normalizeLimits(limits Limits) Limits {
	defaults := DefaultLimits()
	if limits.ScrollbackBytes == 0 {
		limits.ScrollbackBytes = defaults.ScrollbackBytes
	}
	if limits.IdleTimeout == 0 {
		limits.IdleTimeout = defaults.IdleTimeout
	}
	if limits.MaxLifetime == 0 {
		limits.MaxLifetime = defaults.MaxLifetime
	}
	if limits.MaxSessions == 0 {
		limits.MaxSessions = defaults.MaxSessions
	}
	if limits.OutputChunkSize == 0 {
		limits.OutputChunkSize = defaults.OutputChunkSize
	}
	if limits.OutputFlush == 0 {
		limits.OutputFlush = defaults.OutputFlush
	}
	if limits.TerminationGracePeriod == 0 {
		limits.TerminationGracePeriod = defaults.TerminationGracePeriod
	}
	return limits
}
