//go:build !windows

package agentterminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gsd-build/daemon/internal/terminal"
)

type ControlServer struct {
	manager *Manager
	baseDir string
	mu      sync.Mutex
	tasks   map[string]*taskServer
}

type taskServer struct {
	scope  TaskScope
	token  string
	server *http.Server
	ln     net.Listener
	path   string
}

func NewControlServer(manager *Manager, baseDir string) *ControlServer {
	if baseDir == "" {
		baseDir = defaultControlBaseDir()
	}
	return &ControlServer{manager: manager, baseDir: baseDir, tasks: make(map[string]*taskServer)}
}

func (s *ControlServer) StartTask(ctx context.Context, scope TaskScope) (TaskControl, error) {
	if scope.TaskID == "" {
		scope.TaskID = randomID()
	}
	if err := os.MkdirAll(s.baseDir, 0o700); err != nil {
		return TaskControl{}, err
	}
	socketPath := filepath.Join(s.baseDir, "t-"+shortSafeName(scope.TaskID)+"-"+randomID()[:12]+".sock")
	token := bearerToken()
	ts := &taskServer{scope: scope, token: token, path: socketPath}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /background/start", s.authed(ts, s.handleBackgroundStart(ts)))
	mux.HandleFunc("POST /background/output", s.authed(ts, s.handleOutput))
	mux.HandleFunc("POST /background/wait", s.authed(ts, s.handleWait))
	mux.HandleFunc("POST /background/send", s.authed(ts, s.handleSend))
	mux.HandleFunc("POST /background/kill", s.authed(ts, s.handleKill))
	mux.HandleFunc("POST /background/list", s.authed(ts, s.handleList(ts)))
	mux.HandleFunc("POST /shell/exec", s.authed(ts, s.handleShellExec(ts)))
	ts.server = &http.Server{Handler: mux}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return TaskControl{}, err
	}
	ts.ln = ln

	s.mu.Lock()
	if existing := s.tasks[scope.TaskID]; existing != nil {
		go existing.stop()
	}
	s.tasks[scope.TaskID] = ts
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.StopTask(scope.TaskID)
	}()
	go func() {
		_ = ts.server.Serve(ln)
	}()

	return TaskControl{
		SocketPath: socketPath,
		Token:      token,
		Env: []string{
			"GSD_AGENT_TOOLS_SOCKET=" + socketPath,
			"GSD_AGENT_TOOLS_TOKEN=" + token,
			"GSD_SESSION_ID=" + scope.SessionID,
			"GSD_CHANNEL_ID=" + scope.ChannelID,
			"GSD_TASK_ID=" + scope.TaskID,
		},
	}, nil
}

func (s *ControlServer) StopTask(taskID string) {
	s.mu.Lock()
	ts := s.tasks[taskID]
	delete(s.tasks, taskID)
	s.mu.Unlock()
	if ts != nil {
		ts.stop()
	}
}

func (ts *taskServer) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = ts.server.Shutdown(ctx)
	if ts.ln != nil {
		_ = ts.ln.Close()
	}
	if ts.path != "" {
		_ = os.Remove(ts.path)
	}
}

func (s *ControlServer) authed(ts *taskServer, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+ts.token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (s *ControlServer) handleBackgroundStart(ts *taskServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req StartRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		req.SessionID = ts.scope.SessionID
		req.ChannelID = ts.scope.ChannelID
		req.TaskID = ts.scope.TaskID
		req.ProjectID = ts.scope.ProjectID
		req.MachineID = ts.scope.MachineID
		req.ProjectCWD = ts.scope.ProjectCWD
		result, err := s.manager.Start(context.Background(), req)
		writeResult(w, result, err)
	}
}

func (s *ControlServer) handleOutput(w http.ResponseWriter, r *http.Request) {
	var req OutputRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.manager.Output(req)
	writeResult(w, result, err)
}

func (s *ControlServer) handleWait(w http.ResponseWriter, r *http.Request) {
	var req WaitRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.TimeoutMs <= 0 || time.Duration(req.TimeoutMs)*time.Millisecond > s.manager.limits.MaxWaitTimeout {
		req.TimeoutMs = int(s.manager.limits.MaxWaitTimeout / time.Millisecond)
	}
	result, err := s.manager.Wait(r.Context(), req)
	writeResult(w, result, err)
}

func (s *ControlServer) handleSend(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.manager.Send(req)
	writeResult(w, result, err)
}

func (s *ControlServer) handleKill(w http.ResponseWriter, r *http.Request) {
	var req KillRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.manager.Kill(req)
	writeResult(w, result, err)
}

func (s *ControlServer) handleList(ts *taskServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ListRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		result, err := s.manager.List(ts.scope.SessionID, req)
		writeResult(w, result, err)
	}
}

func (s *ControlServer) handleShellExec(ts *taskServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ShellExecRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		mode := strings.TrimSpace(req.Mode)
		if mode == "" {
			mode = "auto"
		}
		if mode == "background" || (mode == "auto" && ShouldRunInBackground(req.Command)) {
			started, err := s.manager.Start(context.Background(), StartRequest{
				Command:    req.Command,
				CWD:        req.CWD,
				SessionID:  ts.scope.SessionID,
				ChannelID:  ts.scope.ChannelID,
				TaskID:     ts.scope.TaskID,
				ToolCallID: req.ToolCallID,
				ProjectID:  ts.scope.ProjectID,
				MachineID:  ts.scope.MachineID,
				ProjectCWD: ts.scope.ProjectCWD,
			})
			writeResult(w, ShellExecResult{Mode: mode, Background: true, Started: &started}, err)
			return
		}
		result, err := runForeground(r.Context(), ts.scope, req, s.manager.limits.ToolOutputBytes)
		writeResult(w, result, err)
	}
}

func runForeground(ctx context.Context, scope TaskScope, req ShellExecRequest, outputLimit int) (ShellExecResult, error) {
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return ShellExecResult{}, fmt.Errorf("command is required")
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > 120*time.Second {
		timeout = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cwd, err := resolveCWD(req.CWD, scope.ProjectCWD)
	if err != nil {
		return ShellExecResult{}, err
	}
	cmd := exec.CommandContext(runCtx, terminal.ResolveShell(), "-lc", command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	stdout := &boundedOutputBuffer{limit: outputLimit}
	stderr := &boundedOutputBuffer{limit: outputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if runCtx.Err() == context.DeadlineExceeded {
			exitCode = -1
		} else {
			return ShellExecResult{}, err
		}
	}
	return ShellExecResult{
		Mode:       "foreground",
		Background: false,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   exitCode,
		TimedOut:   runCtx.Err() == context.DeadlineExceeded,
		Truncated:  stdout.Truncated() || stderr.Truncated(),
	}, nil
}

type boundedOutputBuffer struct {
	limit     int
	data      []byte
	truncated bool
}

func (b *boundedOutputBuffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		b.truncated = true
		return len(p), nil
	}
	if overflow := len(b.data) + len(p) - b.limit; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *boundedOutputBuffer) String() string {
	return string(b.data)
}

func (b *boundedOutputBuffer) Truncated() bool {
	return b.truncated
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func writeResult(w http.ResponseWriter, result any, err error) {
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func bearerToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return randomID()
	}
	return hex.EncodeToString(b[:])
}

func safeName(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "task"
	}
	return b.String()
}

func shortSafeName(value string) string {
	name := safeName(value)
	if len(name) > 24 {
		return name[:24]
	}
	return name
}

func defaultControlBaseDir() string {
	if runtime.GOOS != "windows" {
		if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
			return filepath.Join("/tmp", "gsd-agent-tools")
		}
	}
	return filepath.Join(os.TempDir(), "gsd-agent-tools")
}
