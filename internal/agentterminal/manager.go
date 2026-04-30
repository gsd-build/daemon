//go:build !windows

package agentterminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gsd-build/daemon/internal/terminal"
)

type Manager struct {
	mu           sync.Mutex
	jobs         map[string]*managedJob
	byTerminal   map[string]*managedJob
	events       EventSender
	limits       Limits
	serverSource string
}

type managedJob struct {
	mu             sync.Mutex
	req            StartRequest
	jobID          string
	terminalID     string
	command        string
	commandPreview string
	title          string
	cwd            string
	cmd            *exec.Cmd
	ptmx           *os.File
	ring           *scrollbackRing
	detector       *ReadinessDetector
	status         string
	readiness      Readiness
	ports          []Port
	urls           []string
	startedAt      time.Time
	updatedAt      time.Time
	endedAt        time.Time
	exitCode       *int
	signal         string
	reason         string
	closeOnce      sync.Once
	done           chan struct{}
	notify         chan struct{}
	readyTimer     *time.Timer
	lifetimeTimer  *time.Timer
	reportedPorts  map[int]bool
}

func NewManager(events EventSender, limits Limits) *Manager {
	limits = normalizeLimits(limits)
	return &Manager{
		jobs:         make(map[string]*managedJob),
		byTerminal:   make(map[string]*managedJob),
		events:       events,
		limits:       limits,
		serverSource: "agent_terminal_job",
	}
}

func (m *Manager) Start(ctx context.Context, req StartRequest) (StartResult, error) {
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return StartResult{}, fmt.Errorf("command is required")
	}
	cwd, err := resolveCWD(req.CWD, req.ProjectCWD)
	if err != nil {
		return StartResult{}, err
	}
	detector, err := NewReadinessDetector(req, m.limits)
	if err != nil {
		return StartResult{}, fmt.Errorf("readiness: %w", err)
	}
	if err := m.checkLimits(req.SessionID); err != nil {
		return StartResult{}, err
	}

	shell := terminal.ResolveShell()
	cmd := exec.CommandContext(ctx, shell, "-lc", command)
	cmd.Dir = cwd
	cmd.Env = startEnv(req.Env)
	if runtime.GOOS != "darwin" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 32})
	if err != nil {
		return StartResult{}, fmt.Errorf("start pty: %w", err)
	}

	now := time.Now().UTC()
	status := StatusRunning
	if detector.state.State == ReadinessReady {
		status = StatusReady
	}
	job := &managedJob{
		req:            req,
		jobID:          "job-" + randomID(),
		terminalID:     "term-" + randomID(),
		command:        command,
		commandPreview: commandPreview(command),
		title:          titleFor(req, command),
		cwd:            cwd,
		cmd:            cmd,
		ptmx:           ptmx,
		ring:           newScrollbackRing(m.limits.ScrollbackBytes),
		detector:       detector,
		status:         status,
		readiness:      detector.state,
		ports:          clonePorts(detector.ports),
		urls:           append([]string(nil), detector.urls...),
		startedAt:      now,
		updatedAt:      now,
		done:           make(chan struct{}),
		notify:         make(chan struct{}),
		reportedPorts:  make(map[int]bool),
	}

	m.mu.Lock()
	if err := m.checkLimitsLocked(req.SessionID); err != nil {
		m.mu.Unlock()
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		return StartResult{}, err
	}
	m.jobs[job.jobID] = job
	m.byTerminal[job.terminalID] = job
	m.mu.Unlock()

	snapshot := job.snapshot()
	_ = m.events.SendAgentTerminalStarted(snapshot)
	job.reportDetectedPorts(m.events, m.serverSource)
	go m.readLoop(job)
	go m.waitLoop(job)
	m.startTimers(job)

	select {
	case <-ctx.Done():
	default:
	}

	return StartResult{
		JobID:       snapshot.JobID,
		TerminalID:  snapshot.TerminalID,
		Status:      snapshot.Status,
		Title:       snapshot.Title,
		CWD:         cwd,
		StartedAt:   now.Format(time.RFC3339Nano),
		Readiness:   snapshot.Readiness,
		Ports:       clonePorts(snapshot.Ports),
		URLs:        append([]string(nil), snapshot.URLs...),
		OutputTail:  "",
		NextActions: []string{"background_output", "background_wait", "background_kill"},
	}, nil
}

func (m *Manager) Output(req OutputRequest) (OutputResult, error) {
	job, ok := m.getJob(req.JobID)
	if !ok {
		return OutputResult{}, ErrJobNotFound
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	limit := req.TailBytes
	if limit <= 0 {
		limit = m.limits.ToolOutputBytes
	}
	var out []byte
	var truncated bool
	if req.SinceSeq > 0 {
		out, truncated = job.ring.SinceSeq(req.SinceSeq, limit)
	} else {
		out, truncated = job.ring.TailBytes(limit)
	}
	if req.TailLines > 0 {
		out, truncated = tailLines(out, req.TailLines, truncated)
	}
	return job.outputResultLocked(string(out), truncated), nil
}

func (m *Manager) Wait(ctx context.Context, req WaitRequest) (WaitResult, error) {
	job, ok := m.getJob(req.JobID)
	if !ok {
		return WaitResult{}, ErrJobNotFound
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 || timeout > m.limits.MaxWaitTimeout {
		timeout = m.limits.MaxWaitTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var pattern *regexp.Regexp
	if req.Pattern != "" {
		compiled, err := regexp.Compile(req.Pattern)
		if err != nil {
			return WaitResult{}, err
		}
		pattern = compiled
	}
	for {
		job.mu.Lock()
		result, matched := job.waitResultLocked(req, pattern)
		ch := job.notify
		job.mu.Unlock()
		if matched {
			return result, nil
		}
		select {
		case <-waitCtx.Done():
			job.mu.Lock()
			result, _ = job.waitResultLocked(req, pattern)
			result.TimedOut = true
			job.mu.Unlock()
			return result, nil
		case <-ch:
		}
	}
}

func (m *Manager) Send(req SendRequest) (SendResult, error) {
	job, ok := m.getJob(req.JobID)
	if !ok {
		return SendResult{}, ErrJobNotFound
	}
	input := req.Input
	if req.AppendNewline {
		input += "\n"
	}
	if err := m.Input(job.terminalID, []byte(input)); err != nil {
		return SendResult{}, err
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	return SendResult{JobID: job.jobID, Sent: true, Seq: job.ring.Seq(), Status: job.status}, nil
}

func (m *Manager) Kill(req KillRequest) (KillResult, error) {
	job, ok := m.getJob(req.JobID)
	if !ok {
		return KillResult{}, ErrJobNotFound
	}
	reason := req.Reason
	if reason == "" {
		reason = terminal.ReasonClosedByUser
	}
	m.closeJob(job, reason, req.Signal)
	<-job.done
	job.mu.Lock()
	defer job.mu.Unlock()
	return KillResult{JobID: job.jobID, Status: job.status, ExitCode: job.exitCode, Signal: job.signal, EndedAt: formatTime(job.endedAt)}, nil
}

func (m *Manager) List(sessionID string, req ListRequest) (ListResult, error) {
	m.mu.Lock()
	jobs := make([]*managedJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.Unlock()
	out := ListResult{}
	for _, job := range jobs {
		job.mu.Lock()
		if sessionID != "" && job.req.SessionID != sessionID {
			job.mu.Unlock()
			continue
		}
		if req.Status != "" && job.status != req.Status {
			job.mu.Unlock()
			continue
		}
		out.Jobs = append(out.Jobs, JobSummary{
			JobID:          job.jobID,
			TerminalID:     job.terminalID,
			Title:          job.title,
			CommandPreview: job.commandPreview,
			CWD:            job.cwd,
			Status:         job.status,
			Readiness:      job.readiness,
			Ports:          clonePorts(job.ports),
			URLs:           append([]string(nil), job.urls...),
			StartedAt:      job.startedAt.Format(time.RFC3339Nano),
			EndedAt:        formatTime(job.endedAt),
		})
		job.mu.Unlock()
	}
	return out, nil
}

func (m *Manager) Snapshot(terminalID string) error {
	job, ok := m.getByTerminal(terminalID)
	if !ok {
		return ErrTerminalNotFound
	}
	job.mu.Lock()
	seq := job.ring.Seq()
	data, _ := job.ring.TailBytes(m.limits.ScrollbackBytes)
	sessionID := job.req.SessionID
	channelID := job.req.ChannelID
	job.mu.Unlock()
	return m.events.SendTerminalSnapshot(terminalID, sessionID, channelID, seq, data)
}

func (m *Manager) HasTerminal(terminalID string) bool {
	_, ok := m.getByTerminal(terminalID)
	return ok
}

func (m *Manager) Input(terminalID string, data []byte) error {
	job, ok := m.getByTerminal(terminalID)
	if !ok {
		return ErrTerminalNotFound
	}
	job.mu.Lock()
	ptmx := job.ptmx
	status := job.status
	job.mu.Unlock()
	if ptmx == nil || terminalDone(status) {
		return ErrTerminalClosed
	}
	_, err := ptmx.Write(data)
	return err
}

func (m *Manager) Resize(terminalID string, cols, rows int) error {
	job, ok := m.getByTerminal(terminalID)
	if !ok {
		return ErrTerminalNotFound
	}
	job.mu.Lock()
	ptmx := job.ptmx
	job.mu.Unlock()
	if ptmx == nil {
		return nil
	}
	return pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(clamp(cols, 20, 300)), Rows: uint16(clamp(rows, 5, 100))})
}

func (m *Manager) Close(terminalID string, reason string) {
	job, ok := m.getByTerminal(terminalID)
	if !ok {
		return
	}
	m.closeJob(job, reason, "")
}

func (m *Manager) CloseAll(ctx context.Context, reason string) {
	m.mu.Lock()
	jobs := make([]*managedJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.Unlock()
	for _, job := range jobs {
		m.closeJob(job, reason, "")
	}
	for _, job := range jobs {
		select {
		case <-job.done:
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) readLoop(job *managedJob) {
	buf := make([]byte, m.limits.OutputChunkBytes)
	for {
		n, err := job.ptmx.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			job.mu.Lock()
			seq := job.ring.Write(chunk)
			readiness, ports, urls, advanced := job.detector.Observe(string(chunk))
			if advanced {
				job.readiness = readiness
				job.ports = ports
				job.urls = urls
				if readiness.State == ReadinessReady {
					job.status = StatusReady
				}
				job.updatedAt = time.Now().UTC()
			}
			snapshot := job.snapshotLocked()
			job.notifyLocked()
			job.mu.Unlock()
			_ = m.events.SendTerminalOutput(job.terminalID, job.req.SessionID, job.req.ChannelID, seq, chunk)
			if advanced {
				_ = m.events.SendAgentTerminalUpdated(snapshot)
				job.reportDetectedPorts(m.events, m.serverSource)
			}
		}
		if err != nil {
			if err != io.EOF && !strings.Contains(err.Error(), "file already closed") {
				_ = m.events.SendTerminalError("", job.terminalID, job.req.SessionID, job.req.ChannelID, "Agent terminal stream ended")
			}
			return
		}
	}
}

func (m *Manager) waitLoop(job *managedJob) {
	err := job.cmd.Wait()
	_ = job.ptmx.Close()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
		}
	}
	job.mu.Lock()
	job.stopTimersLocked()
	if job.reason == terminal.ReasonClosedByUser || job.reason == terminal.ReasonDaemonShutdown {
		job.status = StatusKilled
	} else if exitCode == 0 {
		job.status = StatusExited
	} else {
		job.status = StatusFailed
	}
	job.exitCode = &exitCode
	job.endedAt = time.Now().UTC()
	job.updatedAt = job.endedAt
	readiness, advanced := job.detector.MarkExit(exitCode)
	if advanced {
		job.readiness = readiness
	}
	if job.reason == "" {
		job.reason = terminal.ReasonProcessExit
	}
	snapshot := job.snapshotLocked()
	job.notifyLocked()
	job.mu.Unlock()

	m.mu.Lock()
	delete(m.byTerminal, job.terminalID)
	delete(m.jobs, job.jobID)
	m.mu.Unlock()

	_ = m.events.SendAgentTerminalUpdated(snapshot)
	_ = m.events.SendTerminalExit(job.terminalID, job.req.SessionID, job.req.ChannelID, job.reason, exitCode, job.signal, job.endedAt)
	close(job.done)
}

func (m *Manager) startTimers(job *managedJob) {
	job.mu.Lock()
	defer job.mu.Unlock()
	readyTimeout := job.detector.timeout
	if readyTimeout <= 0 {
		readyTimeout = m.limits.DefaultReadyTimeout
	}
	if readyTimeout > 0 {
		job.readyTimer = time.AfterFunc(readyTimeout, func() {
			job.mu.Lock()
			if job.readiness.State == ReadinessWaiting {
				job.readiness.State = ReadinessTimedOut
				job.updatedAt = time.Now().UTC()
				snapshot := job.snapshotLocked()
				job.notifyLocked()
				job.mu.Unlock()
				_ = m.events.SendAgentTerminalUpdated(snapshot)
				return
			}
			job.mu.Unlock()
		})
	}
	if m.limits.MaxLifetime > 0 {
		job.lifetimeTimer = time.AfterFunc(m.limits.MaxLifetime, func() {
			m.closeJob(job, terminal.ReasonMaxLifetime, "")
		})
	}
}

func (m *Manager) closeJob(job *managedJob, reason string, signalName string) {
	if reason == "" {
		reason = terminal.ReasonClosedByUser
	}
	job.closeOnce.Do(func() {
		job.mu.Lock()
		job.reason = reason
		job.signal = signalName
		job.stopTimersLocked()
		ptmx := job.ptmx
		cmd := job.cmd
		job.mu.Unlock()
		if ptmx != nil {
			_ = ptmx.Close()
		}
		if cmd != nil && cmd.Process != nil {
			sig := signalFromName(signalName)
			if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
				_ = cmd.Process.Signal(sig)
			}
			time.AfterFunc(m.limits.TerminationGracePeriod, func() {
				select {
				case <-job.done:
				default:
					if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
						_ = cmd.Process.Kill()
					}
				}
			})
		}
	})
}

func (m *Manager) getJob(jobID string) (*managedJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	return job, ok
}

func (m *Manager) getByTerminal(terminalID string) (*managedJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.byTerminal[terminalID]
	return job, ok
}

func (m *Manager) checkLimits(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkLimitsLocked(sessionID)
}

func (m *Manager) checkLimitsLocked(sessionID string) error {
	activeTotal := 0
	activeSession := 0
	for _, job := range m.jobs {
		job.mu.Lock()
		active := !terminalDone(job.status)
		sameSession := job.req.SessionID == sessionID
		job.mu.Unlock()
		if !active {
			continue
		}
		activeTotal++
		if sameSession {
			activeSession++
		}
	}
	if m.limits.MaxDaemonJobs > 0 && activeTotal >= m.limits.MaxDaemonJobs {
		return fmt.Errorf("agent terminal daemon job limit reached")
	}
	if m.limits.MaxSessionJobs > 0 && activeSession >= m.limits.MaxSessionJobs {
		return fmt.Errorf("agent terminal session job limit reached")
	}
	return nil
}

func (j *managedJob) snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

func (j *managedJob) snapshotLocked() Job {
	return Job{
		JobID:          j.jobID,
		TerminalID:     j.terminalID,
		SessionID:      j.req.SessionID,
		ChannelID:      j.req.ChannelID,
		TaskID:         j.req.TaskID,
		ToolCallID:     j.req.ToolCallID,
		ProjectID:      j.req.ProjectID,
		MachineID:      j.req.MachineID,
		Command:        j.command,
		CommandPreview: j.commandPreview,
		Title:          j.title,
		CWD:            j.cwd,
		Status:         j.status,
		Readiness:      j.readiness,
		Ports:          clonePorts(j.ports),
		URLs:           append([]string(nil), j.urls...),
		Seq:            j.ring.Seq(),
		StartedAt:      j.startedAt,
		UpdatedAt:      j.updatedAt,
		EndedAt:        j.endedAt,
		ExitCode:       j.exitCode,
		Signal:         j.signal,
		Reason:         j.reason,
	}
}

func (j *managedJob) outputResultLocked(output string, truncated bool) OutputResult {
	return OutputResult{
		JobID:      j.jobID,
		TerminalID: j.terminalID,
		Status:     j.status,
		Seq:        j.ring.Seq(),
		Output:     output,
		Truncated:  truncated,
		Readiness:  j.readiness,
		Ports:      clonePorts(j.ports),
		URLs:       append([]string(nil), j.urls...),
		ExitCode:   j.exitCode,
		Signal:     j.signal,
		EndedAt:    formatTime(j.endedAt),
	}
}

func (j *managedJob) waitResultLocked(req WaitRequest, pattern *regexp.Regexp) (WaitResult, bool) {
	out, _ := j.ring.SinceSeq(req.SinceSeq, 64*1024)
	output := string(out)
	matched := false
	switch req.Condition {
	case "", "output":
		matched = pattern != nil && pattern.MatchString(output)
	case "ready":
		matched = j.readiness.State == ReadinessReady
	case "exit", "exited":
		matched = terminalDone(j.status)
	default:
		if pattern != nil {
			matched = pattern.MatchString(output)
		}
	}
	return WaitResult{
		JobID:      j.jobID,
		Matched:    matched,
		Condition:  req.Condition,
		Status:     j.status,
		Seq:        j.ring.Seq(),
		OutputTail: j.ring.StringTail(64 * 1024),
		Readiness:  j.readiness,
	}, matched
}

func (j *managedJob) notifyLocked() {
	close(j.notify)
	j.notify = make(chan struct{})
}

func (j *managedJob) stopTimersLocked() {
	if j.readyTimer != nil {
		j.readyTimer.Stop()
	}
	if j.lifetimeTimer != nil {
		j.lifetimeTimer.Stop()
	}
}

func (j *managedJob) reportDetectedPorts(events EventSender, source string) {
	j.mu.Lock()
	var ports []Port
	for _, p := range j.ports {
		if j.reportedPorts[p.Port] {
			continue
		}
		j.reportedPorts[p.Port] = true
		ports = append(ports, p)
	}
	req := j.req
	command := j.commandPreview
	j.mu.Unlock()
	for _, p := range ports {
		_ = events.SendLocalServerDetected(req.SessionID, req.ChannelID, req.TaskID, req.ToolCallID, "127.0.0.1", p.Port, p.URL, command, source)
	}
}

func resolveCWD(cwd string, projectCWD string) (string, error) {
	selected := strings.TrimSpace(cwd)
	if selected == "" {
		selected = strings.TrimSpace(projectCWD)
	}
	if selected != "" && !filepath.IsAbs(selected) && projectCWD != "" {
		selected = filepath.Join(projectCWD, selected)
	}
	return terminal.ValidateCWD(selected)
}

func startEnv(extra map[string]string) []string {
	env := os.Environ()
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	for k, v := range extra {
		if strings.ContainsAny(k, "=\x00") || k == "" {
			continue
		}
		env = append(env, k+"="+v)
	}
	return env
}

func commandPreview(command string) string {
	command = strings.Join(strings.Fields(command), " ")
	if len(command) > 200 {
		return command[:197] + "..."
	}
	return command
}

func titleFor(req StartRequest, command string) string {
	if title := strings.TrimSpace(req.Title); title != "" {
		return title
	}
	preview := commandPreview(command)
	if len(preview) > 60 {
		return preview[:57] + "..."
	}
	return preview
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func signalFromName(name string) syscall.Signal {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "SIGINT", "INT":
		return syscall.SIGINT
	case "SIGHUP", "HUP":
		return syscall.SIGHUP
	case "SIGKILL", "KILL":
		return syscall.SIGKILL
	default:
		return syscall.SIGTERM
	}
}

func terminalDone(status string) bool {
	return status == StatusExited || status == StatusFailed || status == StatusKilled
}

func tailLines(data []byte, lines int, truncated bool) ([]byte, bool) {
	if lines <= 0 {
		return data, truncated
	}
	count := 0
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			count++
			if count > lines {
				return append([]byte(nil), data[i+1:]...), true
			}
		}
	}
	return data, truncated
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func normalizeLimits(limits Limits) Limits {
	defaults := DefaultLimits()
	if limits.MaxSessionJobs == 0 {
		limits.MaxSessionJobs = defaults.MaxSessionJobs
	}
	if limits.MaxDaemonJobs == 0 {
		limits.MaxDaemonJobs = defaults.MaxDaemonJobs
	}
	if limits.ScrollbackBytes == 0 {
		limits.ScrollbackBytes = defaults.ScrollbackBytes
	}
	if limits.OutputChunkBytes == 0 {
		limits.OutputChunkBytes = defaults.OutputChunkBytes
	}
	if limits.ToolOutputBytes == 0 {
		limits.ToolOutputBytes = defaults.ToolOutputBytes
	}
	if limits.DefaultReadyTimeout == 0 {
		limits.DefaultReadyTimeout = defaults.DefaultReadyTimeout
	}
	if limits.MaxWaitTimeout == 0 {
		limits.MaxWaitTimeout = defaults.MaxWaitTimeout
	}
	if limits.MaxLifetime == 0 {
		limits.MaxLifetime = defaults.MaxLifetime
	}
	if limits.IdleTimeout == 0 {
		limits.IdleTimeout = defaults.IdleTimeout
	}
	if limits.TerminationGracePeriod == 0 {
		limits.TerminationGracePeriod = defaults.TerminationGracePeriod
	}
	return limits
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
