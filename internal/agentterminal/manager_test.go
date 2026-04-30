//go:build !windows

package agentterminal

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/terminal"
)

type captureAgentTerminalEvents struct {
	mu        sync.Mutex
	started   []Job
	updated   []Job
	output    bytes.Buffer
	snapshots [][]byte
	exits     []string
	errors    []string
	servers   []serverEvent
}

type serverEvent struct {
	sessionID  string
	channelID  string
	taskID     string
	toolCallID string
	host       string
	port       int
	url        string
	command    string
	source     string
}

func (c *captureAgentTerminalEvents) SendAgentTerminalStarted(job Job) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started = append(c.started, job)
	return nil
}

func (c *captureAgentTerminalEvents) SendAgentTerminalUpdated(job Job) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updated = append(c.updated, job)
	return nil
}

func (c *captureAgentTerminalEvents) SendTerminalOutput(_, _, _ string, _ int64, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.output.Write(data)
	return nil
}

func (c *captureAgentTerminalEvents) SendTerminalSnapshot(_, _, _ string, _ int64, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshots = append(c.snapshots, append([]byte(nil), data...))
	return nil
}

func (c *captureAgentTerminalEvents) SendTerminalExit(_, _, _, reason string, _ int, _ string, _ time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exits = append(c.exits, reason)
	return nil
}

func (c *captureAgentTerminalEvents) SendTerminalError(_, _, _, _, message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors = append(c.errors, message)
	return nil
}

func (c *captureAgentTerminalEvents) SendLocalServerDetected(sessionID, channelID, taskID, toolCallID, host string, port int, url, command, source string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.servers = append(c.servers, serverEvent{
		sessionID:  sessionID,
		channelID:  channelID,
		taskID:     taskID,
		toolCallID: toolCallID,
		host:       host,
		port:       port,
		url:        url,
		command:    command,
		source:     source,
	})
	return nil
}

func (c *captureAgentTerminalEvents) outputString() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output.String()
}

func (c *captureAgentTerminalEvents) snapshotContains(needle string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, snapshot := range c.snapshots {
		if bytes.Contains(snapshot, []byte(needle)) {
			return true
		}
	}
	return false
}

func (c *captureAgentTerminalEvents) hasExit(reason string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, got := range c.exits {
		if got == reason {
			return true
		}
	}
	return false
}

func (c *captureAgentTerminalEvents) hasReadinessState(state string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, update := range c.updated {
		if update.Readiness.State == state {
			return true
		}
	}
	return false
}

func (c *captureAgentTerminalEvents) firstStarted() (Job, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.started) == 0 {
		return Job{}, false
	}
	return c.started[0], true
}

func testAgentTerminalLimits() Limits {
	return Limits{
		MaxSessionJobs:         2,
		MaxDaemonJobs:          4,
		ScrollbackBytes:        16 * 1024,
		OutputChunkBytes:       1024,
		ToolOutputBytes:        4 * 1024,
		DefaultReadyTimeout:    750 * time.Millisecond,
		MaxWaitTimeout:         2 * time.Second,
		MaxLifetime:            30 * time.Second,
		IdleTimeout:            time.Second,
		TerminationGracePeriod: 50 * time.Millisecond,
	}
}

func setAgentTerminalTestShell(t *testing.T) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
}

func waitForAgentTerminal(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.After(timeout)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", description)
		case <-tick.C:
		}
	}
}

func TestManagerStartWaitOutputListSnapshotAndKill(t *testing.T) {
	setAgentTerminalTestShell(t)
	events := &captureAgentTerminalEvents{}
	manager := NewManager(events, testAgentTerminalLimits())
	defer manager.CloseAll(context.Background(), terminal.ReasonDaemonShutdown)

	result, err := manager.Start(context.Background(), StartRequest{
		Command:      "printf 'boot\\n'; printf 'ready on 3000\\n'; sleep 5",
		CWD:          t.TempDir(),
		ReadyPattern: `ready on \d+`,
		SessionID:    "sess-1",
		ChannelID:    "chan-1",
		TaskID:       "task-1",
		ToolCallID:   "tool-1",
		ProjectID:    "proj-1",
		MachineID:    "machine-1",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if result.JobID == "" || result.TerminalID == "" {
		t.Fatalf("missing ids: %#v", result)
	}

	waitResult, err := manager.Wait(context.Background(), WaitRequest{
		JobID:     result.JobID,
		Condition: "output",
		Pattern:   "ready on 3000",
		TimeoutMs: 1000,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !waitResult.Matched || waitResult.Status != StatusReady {
		t.Fatalf("wait result = %#v", waitResult)
	}

	output, err := manager.Output(OutputRequest{JobID: result.JobID, TailLines: 1})
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !bytes.Contains([]byte(output.Output), []byte("ready on 3000")) {
		t.Fatalf("output = %q", output.Output)
	}
	if output.Readiness.State != ReadinessReady || output.Readiness.Source != "pattern" {
		t.Fatalf("readiness = %#v", output.Readiness)
	}

	jobs, err := manager.List("sess-1", ListRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].JobID != result.JobID {
		t.Fatalf("jobs = %#v", jobs.Jobs)
	}

	if err := manager.Snapshot(result.TerminalID); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	waitForAgentTerminal(t, time.Second, func() bool {
		return events.snapshotContains("ready on 3000")
	}, "terminal snapshot")

	kill, err := manager.Kill(KillRequest{JobID: result.JobID, Reason: terminal.ReasonClosedByUser})
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if kill.Status != StatusKilled || !events.hasExit(terminal.ReasonClosedByUser) {
		t.Fatalf("kill = %#v exits=%#v", kill, events.exits)
	}
}

func TestManagerSendWritesToInteractiveJob(t *testing.T) {
	setAgentTerminalTestShell(t)
	events := &captureAgentTerminalEvents{}
	manager := NewManager(events, testAgentTerminalLimits())
	defer manager.CloseAll(context.Background(), terminal.ReasonDaemonShutdown)

	result, err := manager.Start(context.Background(), StartRequest{
		Command:   "cat",
		CWD:       t.TempDir(),
		SessionID: "sess-1",
		ChannelID: "chan-1",
		TaskID:    "task-1",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := manager.Send(SendRequest{JobID: result.JobID, Input: "agent-input-ok", AppendNewline: true}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	wait, err := manager.Wait(context.Background(), WaitRequest{
		JobID:     result.JobID,
		Condition: "output",
		Pattern:   "agent-input-ok",
		TimeoutMs: 1000,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !wait.Matched {
		t.Fatalf("wait = %#v output=%q", wait, events.outputString())
	}
}

func TestManagerEnforcesSessionJobLimit(t *testing.T) {
	setAgentTerminalTestShell(t)
	events := &captureAgentTerminalEvents{}
	limits := testAgentTerminalLimits()
	limits.MaxSessionJobs = 1
	manager := NewManager(events, limits)
	defer manager.CloseAll(context.Background(), terminal.ReasonDaemonShutdown)

	req := StartRequest{
		Command:   "sleep 5",
		CWD:       t.TempDir(),
		SessionID: "sess-1",
		ChannelID: "chan-1",
		TaskID:    "task-1",
	}
	if _, err := manager.Start(context.Background(), req); err != nil {
		t.Fatalf("Start first: %v", err)
	}
	req.TaskID = "task-2"
	if _, err := manager.Start(context.Background(), req); err == nil {
		t.Fatal("second Start succeeded, want session limit error")
	}
	otherSession, err := manager.List("sess-2", ListRequest{})
	if err != nil {
		t.Fatalf("List other session: %v", err)
	}
	if len(otherSession.Jobs) != 0 {
		t.Fatalf("other session jobs = %#v", otherSession.Jobs)
	}
}

func TestManagerCloseAllStopsRunningJobs(t *testing.T) {
	setAgentTerminalTestShell(t)
	events := &captureAgentTerminalEvents{}
	manager := NewManager(events, testAgentTerminalLimits())
	if _, err := manager.Start(context.Background(), StartRequest{
		Command:   "sleep 5",
		CWD:       t.TempDir(),
		SessionID: "sess-1",
		ChannelID: "chan-1",
		TaskID:    "task-1",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	manager.CloseAll(ctx, terminal.ReasonDaemonShutdown)
	if !events.hasExit(terminal.ReasonDaemonShutdown) {
		t.Fatalf("exits = %#v", events.exits)
	}
}

func TestManagerUsesRequestReadyTimeout(t *testing.T) {
	setAgentTerminalTestShell(t)
	events := &captureAgentTerminalEvents{}
	limits := testAgentTerminalLimits()
	limits.DefaultReadyTimeout = 5 * time.Second
	manager := NewManager(events, limits)
	defer manager.CloseAll(context.Background(), terminal.ReasonDaemonShutdown)

	result, err := manager.Start(context.Background(), StartRequest{
		Command:        "sleep 5",
		CWD:            t.TempDir(),
		SessionID:      "sess-1",
		ChannelID:      "chan-1",
		TaskID:         "task-1",
		ReadyTimeoutMs: 100,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForAgentTerminal(t, time.Second, func() bool {
		return events.hasReadinessState(ReadinessTimedOut)
	}, "request readiness timeout")
	if _, err := manager.Kill(KillRequest{JobID: result.JobID}); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}
