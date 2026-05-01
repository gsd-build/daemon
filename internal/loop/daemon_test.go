package loop

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/gsd-build/daemon/internal/agentterminal"
	"github.com/gsd-build/daemon/internal/browser"
	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/pi"
	"github.com/gsd-build/daemon/internal/preview"
	"github.com/gsd-build/daemon/internal/relay"
	"github.com/gsd-build/daemon/internal/session"
	"github.com/gsd-build/daemon/internal/sockapi"
	"github.com/gsd-build/daemon/internal/terminal"
	protocol "github.com/gsd-build/protocol-go"
)

func requireLoopTestPTY(t *testing.T) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-lc", "printf pty-probe; sleep 0.01")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		if isLoopTestPTYUnavailable(err) {
			t.Skipf("PTY allocation is unavailable on this runner: %v", err)
		}
		t.Fatalf("probe PTY: %v", err)
	}
	_ = ptmx.Close()
	if err := cmd.Wait(); err != nil && !isLoopTestPTYUnavailable(err) && !strings.Contains(strings.ToLower(err.Error()), "hangup") {
		t.Fatalf("probe PTY wait: %v", err)
	}
}

func isLoopTestPTYUnavailable(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "operation not permitted") ||
		strings.Contains(text, "permission denied") ||
		strings.Contains(text, "inappropriate ioctl")
}

func skipIfLoopPTYStartUnavailable(t *testing.T, err error) {
	t.Helper()
	if isLoopTestPTYUnavailable(err) {
		t.Skipf("PTY start is unavailable on this runner: %v", err)
	}
}

func TestRelayURLIncludesMachineIDOnly(t *testing.T) {
	cfg := &config.Config{
		MachineID: "machine-uuid-123",
		AuthToken: "token-with-special/chars+",
		RelayURL:  "wss://relay.example.com/ws/daemon",
	}

	got := buildRelayURL(cfg)

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("machineId") != "machine-uuid-123" {
		t.Errorf("missing or wrong machineId: %q", q.Get("machineId"))
	}
	if q.Get("token") != "" {
		t.Errorf("token must NOT be in URL (leaked to logs); got: %q", q.Get("token"))
	}
	if parsed.Host != "relay.example.com" {
		t.Errorf("unexpected host: %q", parsed.Host)
	}
	if parsed.Path != "/ws/daemon" {
		t.Errorf("unexpected path: %q", parsed.Path)
	}
}

func TestRelayURLPreservesExistingQuery(t *testing.T) {
	cfg := &config.Config{
		MachineID: "machine-uuid-123",
		AuthToken: "token-with-special/chars+",
		RelayURL:  "wss://relay.example.com/ws/daemon?version=2",
	}

	got := buildRelayURL(cfg)

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("machineId") != "machine-uuid-123" {
		t.Errorf("missing or wrong machineId: %q", q.Get("machineId"))
	}
	if q.Get("token") != "" {
		t.Errorf("token must NOT be in URL; got: %q", q.Get("token"))
	}
	if q.Get("version") != "2" {
		t.Errorf("existing query param lost; version: %q", q.Get("version"))
	}
	if parsed.Host != "relay.example.com" {
		t.Errorf("unexpected host: %q", parsed.Host)
	}
	if parsed.Path != "/ws/daemon" {
		t.Errorf("unexpected path: %q", parsed.Path)
	}
}

func TestGracefulShutdownCallsStopAll(t *testing.T) {
	stopped := false
	mgr := &mockManager{stopAllFn: func() { stopped = true }}

	d := &Daemon{
		cfg:     &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version: "test",
		manager: mgr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate shutdown

	d.gracefulShutdown(ctx)

	if !stopped {
		t.Error("expected StopAll to be called during graceful shutdown")
	}
}

func TestStatusUsesRelayConnectionState(t *testing.T) {
	d := &Daemon{
		cfg:       &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version:   "test",
		manager:   &mockManager{},
		client:    relayClientStub(true),
		startedAt: time.Now().Add(-5 * time.Second),
	}

	if !d.Status().RelayConnected {
		t.Fatal("expected status to report connected relay")
	}

	d.client = relayClientStub(false)
	if d.Status().RelayConnected {
		t.Fatal("expected status to report disconnected relay")
	}
}

func TestStatusUsesEffectiveConfiguredConcurrency(t *testing.T) {
	d := &Daemon{
		cfg: &config.Config{
			MachineID:          "m1",
			RelayURL:           "wss://localhost/ws",
			MaxConcurrentTasks: 2,
		},
		version:   "test",
		manager:   &mockManager{},
		client:    relayClientStub(true),
		startedAt: time.Now().Add(-5 * time.Second),
	}

	if got := d.Status().MaxConcurrentTasks; got != 2 {
		t.Fatalf("expected configured max concurrency 2, got %d", got)
	}
}

func TestPreviewHTTPRequestRunsOffRelayReadLoop(t *testing.T) {
	started := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer target.Close()

	d, _ := newTestDaemonWithPreview(t)
	port := mustURLPort(t, target.URL)
	if err := d.previewRegistry.Open(context.Background(), preview.OpenRequest{
		PreviewID: "preview_1",
		SessionID: "session_1",
		ChannelID: "channel_1",
		MachineID: "machine_1",
		Target:    preview.Target{Host: "127.0.0.1", Port: port},
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("open preview: %v", err)
	}
	d.previewHTTP.Client = target.Client()

	start := time.Now()
	if err := d.handleMessage(&protocol.Envelope{
		Type: protocol.MsgTypePreviewHTTPRequest,
		Payload: &protocol.PreviewHTTPRequest{
			Type:      protocol.MsgTypePreviewHTTPRequest,
			RequestID: "req_1",
			StreamID:  "stream_1",
			PreviewID: "preview_1",
			Method:    http.MethodGet,
			Path:      "/",
		},
	}); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("handleMessage blocked for %s", elapsed)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("target request did not start")
	}
	if err := d.handleMessage(&protocol.Envelope{
		Type:    protocol.MsgTypePreviewStreamCancel,
		Payload: &protocol.PreviewStreamCancel{Type: protocol.MsgTypePreviewStreamCancel, StreamID: "stream_1"},
	}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func mustURLPort(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return port
}

func TestScopeRootForChannelRejectsMissingChannel(t *testing.T) {
	d := &Daemon{}

	got, err := d.scopeRootForChannel("browser-channel")
	if err == nil {
		t.Fatalf("scopeRootForChannel missing channel = %q, want error", got)
	}
	if !strings.Contains(err.Error(), "missing channel root") {
		t.Fatalf("error = %q, want missing channel root", err.Error())
	}
}

func TestScopeRootForChannelUsesStoredRoot(t *testing.T) {
	root := t.TempDir()
	d := &Daemon{}
	d.channelRoots.Store("browser-channel", root)

	got, err := d.scopeRootForChannel("browser-channel")
	if err != nil {
		t.Fatalf("scopeRootForChannel: %v", err)
	}
	if got != root {
		t.Fatalf("scopeRootForChannel stored channel = %q, want %q", got, root)
	}
}

func TestHandleBrowseUsesHomeRootForBrowseOnlyChannel(t *testing.T) {
	home := t.TempDir()
	projectPath := filepath.Join(home, "example-project")
	if err := os.Mkdir(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	resolvedProjectPath, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		t.Fatalf("resolve project path: %v", err)
	}
	t.Setenv("HOME", home)

	client := relayClientStub(false)
	d := &Daemon{client: client}

	if err := d.handleBrowse(&protocol.BrowseDir{
		Type:      protocol.MsgTypeBrowseDir,
		RequestID: "req-browse-home",
		ChannelID: "browse-project-picker",
		Path:      "~",
	}); err != nil {
		t.Fatalf("handleBrowse: %v", err)
	}
	env, err := drainQueuedMessage(t, client)
	if err != nil {
		t.Fatalf("drain browse result: %v", err)
	}
	result, ok := env.Payload.(*protocol.BrowseDirResult)
	if !ok {
		t.Fatalf("payload = %T, want BrowseDirResult", env.Payload)
	}
	if !result.OK {
		t.Fatalf("browse result error = %q, want success", result.Error)
	}
	if len(result.Entries) != 1 || result.Entries[0].Path != resolvedProjectPath || !result.Entries[0].IsDirectory {
		t.Fatalf("browse entries = %#v, want example-project directory", result.Entries)
	}
}

func TestHandleMkDirUsesHomeRootForBrowseOnlyChannel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	client := relayClientStub(false)
	d := &Daemon{client: client}
	folderPath := filepath.Join(home, "new-project")

	if err := d.handleMkDir(&protocol.MkDir{
		Type:      protocol.MsgTypeMkDir,
		RequestID: "req-mkdir-home",
		ChannelID: "browse-project-picker",
		Path:      folderPath,
	}); err != nil {
		t.Fatalf("handleMkDir: %v", err)
	}
	env, err := drainQueuedMessage(t, client)
	if err != nil {
		t.Fatalf("drain mkdir result: %v", err)
	}
	result, ok := env.Payload.(*protocol.MkDirResult)
	if !ok {
		t.Fatalf("payload = %T, want MkDirResult", env.Payload)
	}
	if !result.OK {
		t.Fatalf("mkdir result error = %q, want success", result.Error)
	}
	info, err := os.Stat(folderPath)
	if err != nil {
		t.Fatalf("stat created folder: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("created path is not a directory")
	}
}

func TestBrowseOnlyHomeRootRequiresBrowseChannel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	client := relayClientStub(false)
	d := &Daemon{client: client}

	if err := d.handleBrowse(&protocol.BrowseDir{
		Type:      protocol.MsgTypeBrowseDir,
		RequestID: "req-browse-project-channel",
		ChannelID: "project-channel",
		Path:      "~",
	}); err != nil {
		t.Fatalf("handleBrowse: %v", err)
	}
	env, err := drainQueuedMessage(t, client)
	if err != nil {
		t.Fatalf("drain browse result: %v", err)
	}
	browseResult, ok := env.Payload.(*protocol.BrowseDirResult)
	if !ok {
		t.Fatalf("payload = %T, want BrowseDirResult", env.Payload)
	}
	if browseResult.OK || !strings.Contains(browseResult.Error, "missing channel root") {
		t.Fatalf("browse result = %#v, want missing channel root", browseResult)
	}

	if err := d.handleMkDir(&protocol.MkDir{
		Type:      protocol.MsgTypeMkDir,
		RequestID: "req-mkdir-project-channel",
		ChannelID: "project-channel",
		Path:      filepath.Join(home, "new-project"),
	}); err != nil {
		t.Fatalf("handleMkDir: %v", err)
	}
	env, err = drainQueuedMessage(t, client)
	if err != nil {
		t.Fatalf("drain mkdir result: %v", err)
	}
	mkdirResult, ok := env.Payload.(*protocol.MkDirResult)
	if !ok {
		t.Fatalf("payload = %T, want MkDirResult", env.Payload)
	}
	if mkdirResult.OK || !strings.Contains(mkdirResult.Error, "missing channel root") {
		t.Fatalf("mkdir result = %#v, want missing channel root", mkdirResult)
	}
}

func TestHandleReadAllowsExactAgentTouchedFileOutsideScope(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	touchedPath := filepath.Join(outside, "SKILL.md")
	if err := os.WriteFile(touchedPath, []byte("skill body"), 0o644); err != nil {
		t.Fatalf("write touched file: %v", err)
	}
	siblingPath := filepath.Join(outside, "OTHER.md")
	if err := os.WriteFile(siblingPath, []byte("other body"), 0o644); err != nil {
		t.Fatalf("write sibling file: %v", err)
	}

	client := relayClientStub(false)
	d := &Daemon{client: client}
	d.channelRoots.Store("ch-files", root)
	d.recordAgentTouchedFile("ch-files", root, touchedPath)

	if err := d.handleRead(&protocol.ReadFile{
		Type:      protocol.MsgTypeReadFile,
		RequestID: "req-ok",
		ChannelID: "ch-files",
		Path:      touchedPath,
	}); err != nil {
		t.Fatalf("handleRead touched file: %v", err)
	}
	env, err := drainQueuedMessage(t, client)
	if err != nil {
		t.Fatalf("drain touched result: %v", err)
	}
	result, ok := env.Payload.(*protocol.ReadFileResult)
	if !ok {
		t.Fatalf("payload = %T, want ReadFileResult", env.Payload)
	}
	if !result.OK || result.Content != "skill body" {
		t.Fatalf("touched result = %#v, want successful read", result)
	}

	if err := d.handleRead(&protocol.ReadFile{
		Type:      protocol.MsgTypeReadFile,
		RequestID: "req-no",
		ChannelID: "ch-files",
		Path:      siblingPath,
	}); err != nil {
		t.Fatalf("handleRead sibling file: %v", err)
	}
	env, err = drainQueuedMessage(t, client)
	if err != nil {
		t.Fatalf("drain sibling result: %v", err)
	}
	result, ok = env.Payload.(*protocol.ReadFileResult)
	if !ok {
		t.Fatalf("payload = %T, want ReadFileResult", env.Payload)
	}
	if result.OK {
		t.Fatalf("sibling result = %#v, want outside-scope rejection", result)
	}
}

func TestAgentTouchedFileStoreSweepRemovesStaleEntries(t *testing.T) {
	store := &agentTouchedFileStore{}
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

	store.addAt("stale-channel", "/tmp/stale.md", now.Add(-2*time.Hour))
	store.addAt("fresh-channel", "/tmp/fresh.md", now.Add(-10*time.Minute))

	removed := store.sweep(now, time.Hour)

	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if got := store.list("stale-channel"); len(got) != 0 {
		t.Fatalf("stale-channel paths = %v, want empty", got)
	}
	if got := store.list("fresh-channel"); len(got) != 1 || got[0] != "/tmp/fresh.md" {
		t.Fatalf("fresh-channel paths = %v", got)
	}
}

func TestHandleTaskClearsAgentTouchedFilesWhenChannelRootChanges(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	outside := t.TempDir()
	touchedPath := filepath.Join(outside, "SKILL.md")
	if err := os.WriteFile(touchedPath, []byte("skill body"), 0o644); err != nil {
		t.Fatalf("write touched file: %v", err)
	}
	actor, err := session.NewActor(session.Options{
		SessionID: "sess-root-change",
		CWD:       rootB,
		Relay:     newLoopFakeRelay(),
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	d := &Daemon{
		client: relayClientStub(false),
		manager: &mockManager{
			spawnFn: func(ctx context.Context, opts session.Options) (*session.Actor, error) {
				return actor, nil
			},
		},
	}
	d.channelRoots.Store("ch-root-change", rootA)
	d.recordAgentTouchedFile("ch-root-change", rootA, touchedPath)
	if got := d.agentTouchedFiles.list("ch-root-change"); len(got) != 1 {
		t.Fatalf("initial touched files = %v, want one", got)
	}

	err = d.handleTask(&protocol.Task{
		TaskID:    "task-root-change",
		SessionID: "sess-root-change",
		ChannelID: "ch-root-change",
		CWD:       rootB,
		Prompt:    "hello",
	})
	if err != nil {
		t.Fatalf("handleTask: %v", err)
	}

	if got := d.agentTouchedFiles.list("ch-root-change"); len(got) != 0 {
		t.Fatalf("touched files after root change = %v, want empty", got)
	}
}

func TestDefaultPiExtensionPathUsesEnvOverride(t *testing.T) {
	t.Setenv("GSD_PI_EXTENSION_PATH", "/tmp/gsd-pi-extension/index.ts")

	if got := defaultPiExtensionPath(); got != "/tmp/gsd-pi-extension/index.ts" {
		t.Fatalf("expected env override, got %q", got)
	}
}

func TestHandleTaskForcePiSetsEngine(t *testing.T) {
	actor, err := session.NewActor(session.Options{
		SessionID: "sess-force-pi",
		CWD:       t.TempDir(),
		Relay:     newLoopFakeRelay(),
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	d := &Daemon{
		cfg:     &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version: "test",
		manager: &mockManager{
			getFn: func(sessionID string) *session.Actor {
				if sessionID == "sess-force-pi" {
					return actor
				}
				return nil
			},
		},
		client:  relayClientStub(false),
		forcePi: true,
	}

	msg := &protocol.Task{
		TaskID:    "task-force-pi",
		SessionID: "sess-force-pi",
		ChannelID: "ch-force-pi",
		Prompt:    "hello",
		Engine:    "legacy",
	}
	if err := d.handleTask(msg); err != nil {
		t.Fatalf("handleTask: %v", err)
	}
	if msg.Engine != "pi" {
		t.Fatalf("expected forced pi engine, got %q", msg.Engine)
	}
}

func TestHandleTaskUsesSessionScopedBrowserGrant(t *testing.T) {
	browserManager := browser.NewManager(browser.ManagerOptions{
		Service:       loopBrowserService{},
		Sender:        &loopBrowserSender{},
		FrameInterval: time.Hour,
	})
	if err := browserManager.Open(context.Background(), &protocol.BrowserSessionOpen{
		Type:      protocol.MsgTypeBrowserSessionOpen,
		RequestID: "req_1",
		GrantID:   "grant_session",
		SessionID: "sess-browser",
		ChannelID: "ch-browser",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("open browser grant: %v", err)
	}
	t.Cleanup(func() {
		_ = browserManager.Close(context.Background(), &protocol.BrowserSessionClose{
			Type:      protocol.MsgTypeBrowserSessionClose,
			GrantID:   "grant_session",
			BrowserID: "grant_session",
			SessionID: "sess-browser",
			ChannelID: "ch-browser",
			Reason:    "test",
		})
	})

	var captured session.Options
	d := &Daemon{
		cfg:            &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version:        "test",
		client:         relayClientStub(false),
		browserManager: browserManager,
		manager: &mockManager{
			spawnFn: func(ctx context.Context, opts session.Options) (*session.Actor, error) {
				captured = opts
				actor, err := session.NewActor(session.Options{
					SessionID: opts.SessionID,
					CWD:       opts.CWD,
					Relay:     newLoopFakeRelay(),
				})
				if err != nil {
					return nil, err
				}
				t.Cleanup(func() { _ = actor.Stop() })
				return actor, nil
			},
		},
	}

	if err := d.handleTask(&protocol.Task{
		TaskID:    "task-after-browser-open",
		SessionID: "sess-browser",
		ChannelID: "ch-browser",
		CWD:       t.TempDir(),
		Prompt:    "use browser",
	}); err != nil {
		t.Fatalf("handleTask: %v", err)
	}
	if captured.BrowserGrantID != "grant_session" {
		t.Fatalf("BrowserGrantID = %q, want grant_session", captured.BrowserGrantID)
	}
	if captured.BrowserID != "grant_session" {
		t.Fatalf("BrowserID = %q, want grant_session", captured.BrowserID)
	}
}

func TestBrowserRPCEnsuresSessionAndRunsTool(t *testing.T) {
	browserManager := browser.NewManager(browser.ManagerOptions{
		Service:       loopBrowserService{},
		Sender:        &loopBrowserSender{},
		FrameInterval: time.Hour,
	})
	d := &Daemon{browserManager: browserManager}

	resp := d.handleBrowserRPC(context.Background(), browserRPCRequest{
		JSONRPC: "2.0",
		Method:  "browser_tool",
		Params: browser.ToolRPCRequest{
			GrantID:   "grant_rpc",
			SessionID: "sess-rpc",
			TaskID:    "task-rpc",
			ChannelID: "ch-rpc",
			ProjectID: "project-rpc",
			MachineID: "machine-rpc",
			ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
			ToolUseID: "tool-rpc",
			Method:    "snapshot",
		},
	})
	if resp.Error != nil {
		t.Fatalf("rpc error = %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("rpc result = %#v", resp.Result)
	}
	grant, ok := browserManager.GrantForTask("task-rpc")
	if !ok {
		t.Fatal("expected lazy browser grant")
	}
	if grant.BrowserID != "grant_rpc" || grant.ChannelID != "ch-rpc" {
		t.Fatalf("grant = %+v", grant)
	}
}

func TestDaemonHandlesPreviewOpen(t *testing.T) {
	daemon, relayClient := newTestDaemonWithPreview(t)
	err := daemon.handleMessage(&protocol.Envelope{
		Type: protocol.MsgTypePreviewOpen,
		Payload: &protocol.PreviewOpen{
			Type:       protocol.MsgTypePreviewOpen,
			RequestID:  "req_1",
			PreviewID:  "preview_1",
			SessionID:  "session_1",
			ChannelID:  "channel_1",
			MachineID:  "machine_1",
			TargetHost: "127.0.0.1",
			TargetPort: 3000,
			ExpiresAt:  time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	env, err := drainQueuedMessage(t, relayClient)
	if err != nil {
		t.Fatalf("drain relay message: %v", err)
	}
	result, ok := env.Payload.(*protocol.PreviewOpenResult)
	if !ok {
		t.Fatalf("payload = %T, want PreviewOpenResult", env.Payload)
	}
	if !result.OK {
		t.Fatalf("OK=false error=%s", result.ErrorCode)
	}
}

func TestDaemonRejectsUnsafePreviewOpen(t *testing.T) {
	daemon, relayClient := newTestDaemonWithPreview(t)
	if err := daemon.handleMessage(&protocol.Envelope{
		Type: protocol.MsgTypePreviewOpen,
		Payload: &protocol.PreviewOpen{
			Type:       protocol.MsgTypePreviewOpen,
			RequestID:  "req_1",
			PreviewID:  "preview_1",
			SessionID:  "session_1",
			ChannelID:  "channel_1",
			MachineID:  "machine_1",
			TargetHost: "example.com",
			TargetPort: 3000,
			ExpiresAt:  time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	env, err := drainQueuedMessage(t, relayClient)
	if err != nil {
		t.Fatalf("drain relay message: %v", err)
	}
	result, ok := env.Payload.(*protocol.PreviewOpenResult)
	if !ok {
		t.Fatalf("payload = %T, want PreviewOpenResult", env.Payload)
	}
	if result.OK || result.ErrorCode != "unsafe_target" {
		t.Fatalf("result = %#v, want unsafe_target failure", result)
	}
}

func TestHandleTaskSpawnsWithPiSettings(t *testing.T) {
	var got session.Options
	actor, err := session.NewActor(session.Options{
		SessionID: "sess-spawn-pi",
		CWD:       t.TempDir(),
		Relay:     newLoopFakeRelay(),
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	d := &Daemon{
		cfg:             &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version:         "test",
		client:          relayClientStub(false),
		piBinaryPath:    "/opt/gsd/pi",
		piExtensionPath: "/opt/gsd/pi-extension/index.ts",
		manager: &mockManager{
			spawnFn: func(ctx context.Context, opts session.Options) (*session.Actor, error) {
				got = opts
				return actor, nil
			},
		},
	}

	msg := &protocol.Task{
		TaskID:    "task-spawn-pi",
		SessionID: "sess-spawn-pi",
		ChannelID: "ch-spawn-pi",
		CWD:       t.TempDir(),
		Prompt:    "hello",
	}
	if err := d.handleTask(msg); err != nil {
		t.Fatalf("handleTask: %v", err)
	}
	if got.PiBinaryPath != "/opt/gsd/pi" {
		t.Fatalf("expected pi binary path in spawn options, got %q", got.PiBinaryPath)
	}
	if got.PiExtensionPath != "/opt/gsd/pi-extension/index.ts" {
		t.Fatalf("expected pi extension path in spawn options, got %q", got.PiExtensionPath)
	}
}

func TestHandleTerminalMessagesOpenInputAndExit(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	client := relayClientStub(false)
	d := &Daemon{
		cfg:             &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version:         "test",
		manager:         &mockManager{},
		client:          client,
		terminalManager: terminal.NewManager(terminalRelaySender{client: client}, terminal.DefaultLimits()),
	}

	err := d.handleMessage(&protocol.Envelope{Payload: &protocol.TerminalOpen{
		Type:       protocol.MsgTypeTerminalOpen,
		RequestID:  "open-1",
		TerminalID: "term-1",
		SessionID:  "sess-1",
		ChannelID:  "chan-1",
		CWD:        t.TempDir(),
		Cols:       80,
		Rows:       24,
	}})
	if err != nil {
		t.Fatalf("terminal open: %v", err)
	}

	env, err := drainQueuedMessage(t, client)
	if err != nil {
		t.Fatalf("drain opened: %v", err)
	}
	if opened, ok := env.Payload.(*protocol.TerminalOpened); !ok || opened.TerminalID != "term-1" {
		t.Fatalf("opened payload = %#v", env.Payload)
	}

	input := base64.StdEncoding.EncodeToString([]byte("printf gsd-daemon-terminal\nexit\n"))
	if err := d.handleMessage(&protocol.Envelope{Payload: &protocol.TerminalInput{
		Type:       protocol.MsgTypeTerminalInput,
		TerminalID: "term-1",
		ChannelID:  "chan-1",
		DataBase64: input,
	}}); err != nil {
		t.Fatalf("terminal input: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("terminal exit was not queued")
		default:
		}
		env, err := drainQueuedMessage(t, client)
		if err != nil {
			t.Fatalf("drain terminal frame: %v", err)
		}
		if exit, ok := env.Payload.(*protocol.TerminalExit); ok && exit.TerminalID == "term-1" {
			return
		}
	}
}

func TestHandleAgentTerminalAttachSendsSnapshot(t *testing.T) {
	requireLoopTestPTY(t)
	t.Setenv("SHELL", "/bin/sh")
	client := relayClientStub(false)
	manager := newLoopAgentTerminalManager(client)
	defer manager.CloseAll(context.Background(), terminal.ReasonDaemonShutdown)
	d := &Daemon{agentTerminalManager: manager}

	started, err := manager.Start(context.Background(), agentterminal.StartRequest{
		Command:   "printf 'agent-snapshot-ok\\n'; sleep 5",
		CWD:       t.TempDir(),
		SessionID: "sess-agent",
		ChannelID: "chan-agent",
		TaskID:    "task-agent",
	})
	if err != nil {
		skipIfLoopPTYStartUnavailable(t, err)
		t.Fatalf("start agent terminal: %v", err)
	}
	drainQueuedUntil(t, client, func(env *protocol.Envelope) bool {
		output, ok := env.Payload.(*protocol.TerminalOutput)
		if !ok || output.TerminalID != started.TerminalID {
			return false
		}
		data, _ := base64.StdEncoding.DecodeString(output.DataBase64)
		return strings.Contains(string(data), "agent-snapshot-ok")
	})

	if err := d.handleMessage(&protocol.Envelope{Payload: &protocol.AgentTerminalAttach{
		Type:       protocol.MsgTypeAgentTerminalAttach,
		TerminalID: started.TerminalID,
		ChannelID:  "chan-agent",
	}}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	drainQueuedUntil(t, client, func(env *protocol.Envelope) bool {
		snapshot, ok := env.Payload.(*protocol.TerminalSnapshot)
		if !ok || snapshot.TerminalID != started.TerminalID {
			return false
		}
		data, _ := base64.StdEncoding.DecodeString(snapshot.DataBase64)
		return strings.Contains(string(data), "agent-snapshot-ok")
	})
}

func TestHandleTerminalInputRoutesAgentTerminal(t *testing.T) {
	requireLoopTestPTY(t)
	t.Setenv("SHELL", "/bin/sh")
	client := relayClientStub(false)
	manager := newLoopAgentTerminalManager(client)
	defer manager.CloseAll(context.Background(), terminal.ReasonDaemonShutdown)
	d := &Daemon{
		agentTerminalManager: manager,
		terminalManager:      terminal.NewManager(terminalRelaySender{client: client}, terminal.DefaultLimits()),
	}

	started, err := manager.Start(context.Background(), agentterminal.StartRequest{
		Command:   "cat",
		CWD:       t.TempDir(),
		SessionID: "sess-agent",
		ChannelID: "chan-agent",
		TaskID:    "task-agent",
	})
	if err != nil {
		skipIfLoopPTYStartUnavailable(t, err)
		t.Fatalf("start agent terminal: %v", err)
	}
	input := base64.StdEncoding.EncodeToString([]byte("agent-input-route\n"))
	if err := d.handleMessage(&protocol.Envelope{Payload: &protocol.TerminalInput{
		Type:       protocol.MsgTypeTerminalInput,
		TerminalID: started.TerminalID,
		ChannelID:  "chan-agent",
		DataBase64: input,
	}}); err != nil {
		t.Fatalf("terminal input: %v", err)
	}
	drainQueuedUntil(t, client, func(env *protocol.Envelope) bool {
		output, ok := env.Payload.(*protocol.TerminalOutput)
		if !ok || output.TerminalID != started.TerminalID {
			return false
		}
		data, _ := base64.StdEncoding.DecodeString(output.DataBase64)
		return strings.Contains(string(data), "agent-input-route")
	})
}

func TestGracefulShutdownClosesAgentTerminalJobs(t *testing.T) {
	requireLoopTestPTY(t)
	t.Setenv("SHELL", "/bin/sh")
	client := relayClientStub(false)
	manager := newLoopAgentTerminalManager(client)
	d := &Daemon{
		cfg:                  &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version:              "test",
		manager:              &mockManager{},
		client:               client,
		agentTerminalManager: manager,
	}
	if _, err := manager.Start(context.Background(), agentterminal.StartRequest{
		Command:   "sleep 5",
		CWD:       t.TempDir(),
		SessionID: "sess-agent",
		ChannelID: "chan-agent",
		TaskID:    "task-agent",
	}); err != nil {
		skipIfLoopPTYStartUnavailable(t, err)
		t.Fatalf("start agent terminal: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.gracefulShutdown(ctx)

	drainQueuedUntil(t, client, func(env *protocol.Envelope) bool {
		exit, ok := env.Payload.(*protocol.TerminalExit)
		return ok && exit.Reason == terminal.ReasonDaemonShutdown
	})
}

func TestCheckAndRefreshTokenUpdatesLiveClients(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const (
		oldToken  = "old-token"
		newToken  = "new-token"
		machineID = "machine-123"
	)

	var wsAuthHeader string
	var uploadAuthHeader string
	var uploadMachineID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/refresh-token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"authToken":%q,"tokenExpiresAt":"2099-01-01T00:00:00Z"}`, newToken)))
		case "/ws/daemon":
			wsAuthHeader = r.Header.Get("Authorization")
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept websocket: %v", err)
				return
			}
			defer conn.CloseNow()

			if _, _, err := conn.Read(r.Context()); err != nil {
				t.Errorf("read hello: %v", err)
				return
			}

			buf := []byte(`{"type":"welcome"}`)
			if err := conn.Write(r.Context(), websocket.MessageText, buf); err != nil {
				t.Errorf("write welcome: %v", err)
			}
		case "/internal/upload":
			uploadAuthHeader = r.Header.Get("Authorization")
			uploadMachineID = r.Header.Get("X-Machine-Id")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"url":"https://example.invalid/uploaded.png"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	relayURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/daemon"
	cfg := &config.Config{
		MachineID:      machineID,
		AuthToken:      oldToken,
		TokenExpiresAt: time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		ServerURL:      server.URL,
		RelayURL:       relayURL,
	}

	d, err := NewWithPiBinaryPath(cfg, "test-version", "")
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}

	d.checkAndRefreshToken()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := d.client.Connect(ctx, nil); err != nil {
		t.Fatalf("connect after refresh: %v", err)
	}
	if wsAuthHeader != "Bearer "+newToken {
		t.Fatalf("expected websocket auth header %q, got %q", "Bearer "+newToken, wsAuthHeader)
	}

	if _, err := d.uploader.Upload(ctx, "screenshot.png", []byte("img")); err != nil {
		t.Fatalf("upload after refresh: %v", err)
	}
	if uploadAuthHeader != "Bearer "+newToken {
		t.Fatalf("expected upload auth header %q, got %q", "Bearer "+newToken, uploadAuthHeader)
	}
	if uploadMachineID != machineID {
		t.Fatalf("expected upload machine id %q, got %q", machineID, uploadMachineID)
	}
	if d.cfg.AuthToken != newToken {
		t.Fatalf("expected daemon config token %q, got %q", newToken, d.cfg.AuthToken)
	}
}

// mockManager implements SessionManager for testing.
type mockManager struct {
	stopAllFn func()
	getFn     func(sessionID string) *session.Actor
	spawnFn   func(ctx context.Context, opts session.Options) (*session.Actor, error)
}

func (m *mockManager) Get(sessionID string) *session.Actor {
	if m.getFn != nil {
		return m.getFn(sessionID)
	}
	return nil
}
func (m *mockManager) Spawn(ctx context.Context, opts session.Options) (*session.Actor, error) {
	if m.spawnFn != nil {
		return m.spawnFn(ctx, opts)
	}
	return nil, nil
}
func (m *mockManager) ActiveTaskIDs() []string                                                    { return nil }
func (m *mockManager) ActiveCount() (total int, executing int)                                    { return 0, 0 }
func (m *mockManager) InFlightCount() int                                                         { return 0 }
func (m *mockManager) StartReaper(ctx context.Context, tick time.Duration, maxIdle time.Duration) {}
func (m *mockManager) StartWorkerReaper(ctx context.Context, tick time.Duration, maxIdle time.Duration, idleCap int) {
}
func (m *mockManager) StopAll() {
	if m.stopAllFn != nil {
		m.stopAllFn()
	}
}
func (m *mockManager) SessionInfos() []sockapi.SessionInfo  { return nil }
func (m *mockManager) WorkerSnapshots() []pi.WorkerSnapshot { return nil }

type loopBrowserService struct{}

func (loopBrowserService) Open(ctx context.Context, req browser.OpenRequest) (browser.OpenResult, error) {
	return browser.OpenResult{BrowserID: req.GrantID, URL: "about:blank", Title: "Blank"}, nil
}

func (loopBrowserService) Close(ctx context.Context, browserID string) error {
	return nil
}

func (loopBrowserService) Frame(ctx context.Context, browserID string) (browser.Frame, error) {
	return browser.Frame{
		Sequence:    1,
		ContentType: "image/jpeg",
		DataBase64:  "aGVsbG8=",
		Width:       1280,
		Height:      720,
		CapturedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		URL:         "about:blank",
		Title:       "Blank",
	}, nil
}

func (loopBrowserService) Refs(ctx context.Context, browserID string) (browser.Refs, error) {
	return browser.Refs{
		Version:    1,
		Refs:       nil,
		CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func (loopBrowserService) Tool(ctx context.Context, browserID string, method string, params []byte) (browser.ToolResult, error) {
	return browser.ToolResult{OK: true, ResultJSON: []byte(`{"ok":true}`)}, nil
}

func (loopBrowserService) UserInput(ctx context.Context, browserID string, input *protocol.BrowserUserInput) error {
	return nil
}

type loopBrowserSender struct{}

func (loopBrowserSender) Send(ctx context.Context, msg any) error {
	return nil
}

func relayClientStub(connected bool) *relay.Client {
	c := relay.NewClient(relay.Config{
		URL:           "wss://relay.example.com/ws/daemon",
		AuthToken:     "token",
		MachineID:     "m1",
		DaemonVersion: "test",
		OS:            "darwin",
		Arch:          "arm64",
	})
	c.SetConnectedForTest(connected)
	return c
}

func newTestDaemonWithPreview(t *testing.T) (*Daemon, *relay.Client) {
	t.Helper()
	client := relayClientStub(false)
	registry := preview.NewRegistry()
	d := &Daemon{
		cfg:             &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version:         "test",
		manager:         &mockManager{},
		client:          client,
		previewRegistry: registry,
		previewHTTP:     &preview.HTTPHandler{Registry: registry, Sender: client},
		previewWS:       preview.NewWebSocketBridge(registry, client),
	}
	return d, client
}

func newLoopAgentTerminalManager(client *relay.Client) *agentterminal.Manager {
	limits := agentterminal.DefaultLimits()
	limits.DefaultReadyTimeout = time.Second
	limits.MaxWaitTimeout = time.Second
	limits.TerminationGracePeriod = 50 * time.Millisecond
	return agentterminal.NewManager(agentTerminalRelaySender{client: client}, limits)
}

func drainQueuedMessage(t *testing.T, client *relay.Client) (*protocol.Envelope, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return client.DrainQueuedForTest(ctx)
}

func drainQueuedUntil(t *testing.T, client *relay.Client, match func(*protocol.Envelope) bool) *protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		env, err := drainQueuedMessage(t, client)
		if err != nil {
			t.Fatalf("drain queued message: %v", err)
		}
		if match(env) {
			return env
		}
	}
	t.Fatal("matching relay message was not queued")
	return nil
}

type loopFakeRelay struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames []any
}

func newLoopFakeRelay() *loopFakeRelay {
	r := &loopFakeRelay{}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *loopFakeRelay) Send(ctx context.Context, msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	r.cond.Broadcast()
	return nil
}

func (r *loopFakeRelay) countTaskCompletes(taskID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, frame := range r.frames {
		if tc, ok := frame.(*protocol.TaskComplete); ok && tc.TaskID == taskID {
			count++
		}
	}
	return count
}

func (r *loopFakeRelay) waitForTaskStarted(t *testing.T, timeout time.Duration, taskID string) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		for _, frame := range r.frames {
			if ts, ok := frame.(*protocol.TaskStarted); ok && ts.TaskID == taskID {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		r.cond.Wait()
	}
}

func writeFakePiBinary(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "fake-pi")
	script := `#!/bin/sh
IFS= read -r _prompt || true
if [ -n "$FAKE_PI_SLEEP" ]; then
  sleep "$FAKE_PI_SLEEP"
fi
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return path
}

func writeFakePiExtension(t *testing.T) string {
	t.Helper()
	extensionPath := filepath.Join(t.TempDir(), "index.ts")
	if err := os.WriteFile(extensionPath, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write fake pi extension: %v", err)
	}
	return extensionPath
}

func TestHandleTaskIgnoresDuplicateTaskID(t *testing.T) {
	piPath := writeFakePiBinary(t)
	t.Setenv("FAKE_PI_SLEEP", "1")

	relaySink := newLoopFakeRelay()
	actor, err := session.NewActor(session.Options{
		SessionID:       "sess-dup",
		CWD:             t.TempDir(),
		Relay:           relaySink,
		PiBinaryPath:    piPath,
		PiExtensionPath: writeFakePiExtension(t),
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	d := &Daemon{
		cfg:     &config.Config{MachineID: "m1", RelayURL: "wss://localhost/ws"},
		version: "test",
		manager: &mockManager{
			getFn: func(sessionID string) *session.Actor {
				if sessionID == "sess-dup" {
					return actor
				}
				return nil
			},
		},
		client: relayClientStub(false),
	}

	msg := &protocol.Task{
		TaskID:    "dup-task-1",
		SessionID: "sess-dup",
		ChannelID: "ch-dup",
		Prompt:    "hello",
	}
	if err := d.handleTask(msg); err != nil {
		t.Fatalf("first handleTask: %v", err)
	}
	if !relaySink.waitForTaskStarted(t, 2*time.Second, "dup-task-1") {
		t.Fatal("expected task to start")
	}
	if err := d.handleTask(msg); err != nil {
		t.Fatalf("duplicate handleTask: %v", err)
	}

	time.Sleep(3500 * time.Millisecond)

	if got := relaySink.countTaskCompletes("dup-task-1"); got != 1 {
		t.Fatalf("expected duplicate task to be ignored, got %d TaskComplete frames", got)
	}
}
