//go:build !windows

package agentterminal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/terminal"
)

func unixSocketClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func shortControlDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), "agt-"+randomID()[:12])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create control dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}

func postAgentTool(t *testing.T, client *http.Client, path string, token string, body any, dst any) int {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://agent-tools"+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	bodyText := string(bodyBytes)
	if resp.StatusCode >= http.StatusBadRequest && isPTYUnavailableMessage(bodyText) {
		t.Skipf("PTY start is unavailable on this runner: %s", bodyText)
	}
	if dst != nil {
		if err := json.Unmarshal(bodyBytes, dst); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return resp.StatusCode
}

func TestControlServerRejectsMissingAndWrongBearer(t *testing.T) {
	setAgentTerminalTestShell(t)
	events := &captureAgentTerminalEvents{}
	manager := NewManager(events, testAgentTerminalLimits())
	server := NewControlServer(manager, shortControlDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer manager.CloseAll(context.Background(), terminal.ReasonDaemonShutdown)

	control, err := server.StartTask(ctx, TaskScope{
		SessionID:  "sess-1",
		ChannelID:  "chan-1",
		TaskID:     "task-1",
		ProjectCWD: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	client := unixSocketClient(control.SocketPath)

	var body map[string]string
	if status := postAgentTool(t, client, "/background/list", "", ListRequest{}, &body); status != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d body=%#v", status, body)
	}
	if status := postAgentTool(t, client, "/background/list", "wrong", ListRequest{}, &body); status != http.StatusUnauthorized {
		t.Fatalf("wrong bearer status = %d body=%#v", status, body)
	}
}

func TestControlServerStartsScopedJobsAndListsSession(t *testing.T) {
	setAgentTerminalTestShell(t)
	events := &captureAgentTerminalEvents{}
	manager := NewManager(events, testAgentTerminalLimits())
	server := NewControlServer(manager, shortControlDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer manager.CloseAll(context.Background(), terminal.ReasonDaemonShutdown)

	projectCWD := t.TempDir()
	expectedCWD, err := filepath.EvalSymlinks(projectCWD)
	if err != nil {
		t.Fatalf("resolve project cwd: %v", err)
	}
	control, err := server.StartTask(ctx, TaskScope{
		SessionID:  "sess-1",
		ChannelID:  "chan-1",
		TaskID:     "task-1",
		ProjectID:  "proj-1",
		MachineID:  "machine-1",
		ProjectCWD: projectCWD,
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	client := unixSocketClient(control.SocketPath)

	var started StartResult
	status := postAgentTool(t, client, "/background/start", control.Token, StartRequest{
		Command:      "printf 'ready at http://localhost:4567/\\n'; sleep 5",
		CWD:          ".",
		ReadyPattern: "ready at",
	}, &started)
	if status != http.StatusOK {
		t.Fatalf("start status = %d started=%#v", status, started)
	}
	if started.JobID == "" || started.TerminalID == "" {
		t.Fatalf("started = %#v", started)
	}

	waitForAgentTerminal(t, time.Second, func() bool {
		job, ok := events.firstStarted()
		return ok &&
			job.SessionID == "sess-1" &&
			job.ChannelID == "chan-1" &&
			job.TaskID == "task-1" &&
			job.ProjectID == "proj-1" &&
			job.MachineID == "machine-1" &&
			job.CWD == expectedCWD
	}, "scoped start event")

	var listed ListResult
	status = postAgentTool(t, client, "/background/list", control.Token, ListRequest{}, &listed)
	if status != http.StatusOK {
		t.Fatalf("list status = %d listed=%#v", status, listed)
	}
	if len(listed.Jobs) != 1 || listed.Jobs[0].JobID != started.JobID {
		t.Fatalf("listed jobs = %#v", listed.Jobs)
	}

	var waited WaitResult
	status = postAgentTool(t, client, "/background/wait", control.Token, WaitRequest{
		JobID:     started.JobID,
		Condition: "output",
		Pattern:   "ready at",
		TimeoutMs: 1000,
	}, &waited)
	if status != http.StatusOK {
		t.Fatalf("wait status = %d waited=%#v", status, waited)
	}
	if !waited.Matched {
		t.Fatalf("waited = %#v", waited)
	}

	var output OutputResult
	status = postAgentTool(t, client, "/background/output", control.Token, OutputRequest{JobID: started.JobID, TailBytes: 1024}, &output)
	if status != http.StatusOK {
		t.Fatalf("output status = %d output=%#v", status, output)
	}
	if !strings.Contains(output.Output, "ready at") {
		t.Fatalf("output = %q", output.Output)
	}

	var killed KillResult
	status = postAgentTool(t, client, "/background/kill", control.Token, KillRequest{JobID: started.JobID}, &killed)
	if status != http.StatusOK {
		t.Fatalf("kill status = %d killed=%#v", status, killed)
	}
	if killed.Status != StatusKilled {
		t.Fatalf("killed = %#v", killed)
	}
}

func TestControlServerShellExecForegroundAndAutoBackground(t *testing.T) {
	setAgentTerminalTestShell(t)
	events := &captureAgentTerminalEvents{}
	manager := NewManager(events, testAgentTerminalLimits())
	server := NewControlServer(manager, shortControlDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer manager.CloseAll(context.Background(), terminal.ReasonDaemonShutdown)

	control, err := server.StartTask(ctx, TaskScope{
		SessionID:  "sess-1",
		ChannelID:  "chan-1",
		TaskID:     "task-1",
		ProjectCWD: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	client := unixSocketClient(control.SocketPath)

	var foreground ShellExecResult
	status := postAgentTool(t, client, "/shell/exec", control.Token, ShellExecRequest{
		Command:   "printf shell-ok",
		Mode:      "foreground",
		TimeoutMs: 1000,
	}, &foreground)
	if status != http.StatusOK {
		t.Fatalf("foreground status = %d result=%#v", status, foreground)
	}
	if foreground.Background || foreground.Stdout != "shell-ok" || foreground.ExitCode != 0 {
		t.Fatalf("foreground = %#v", foreground)
	}

	var background ShellExecResult
	status = postAgentTool(t, client, "/shell/exec", control.Token, ShellExecRequest{
		Command: "tail -f /dev/null",
		Mode:    "auto",
	}, &background)
	if status != http.StatusOK {
		t.Fatalf("background status = %d result=%#v", status, background)
	}
	if !background.Background || background.Started == nil || background.Started.JobID == "" {
		t.Fatalf("background = %#v", background)
	}

	var killed KillResult
	status = postAgentTool(t, client, "/background/kill", control.Token, KillRequest{JobID: background.Started.JobID}, &killed)
	if status != http.StatusOK || killed.Status != StatusKilled {
		t.Fatalf("kill status = %d killed=%#v", status, killed)
	}
}

func TestBoundedOutputBufferKeepsTail(t *testing.T) {
	buf := &boundedOutputBuffer{limit: 3}
	if _, err := buf.Write([]byte("abc")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := buf.Write([]byte("def")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if buf.String() != "def" || !buf.Truncated() {
		t.Fatalf("buffer = %q truncated=%v", buf.String(), buf.Truncated())
	}
}
