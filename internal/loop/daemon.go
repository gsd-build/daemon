// Package loop contains the main daemon event loop: connect to relay,
// dispatch incoming messages to the session manager, run periodic heartbeats.
package loop

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gsd-build/daemon/internal/api"
	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/fs"
	"github.com/gsd-build/daemon/internal/pidfile"
	"github.com/gsd-build/daemon/internal/preview"
	"github.com/gsd-build/daemon/internal/relay"
	"github.com/gsd-build/daemon/internal/session"
	"github.com/gsd-build/daemon/internal/sockapi"
	"github.com/gsd-build/daemon/internal/terminal"
	"github.com/gsd-build/daemon/internal/update"
	"github.com/gsd-build/daemon/internal/upload"
	protocol "github.com/gsd-build/protocol-go"
)

// SessionManager is the interface the Daemon uses to interact with session actors.
type SessionManager interface {
	Get(sessionID string) *session.Actor
	Spawn(ctx context.Context, opts session.Options) (*session.Actor, error)
	ActiveTaskIDs() []string
	ActiveCount() (total int, executing int)
	InFlightCount() int
	StartReaper(ctx context.Context, tick time.Duration, maxIdle time.Duration)
	StopAll()
	SessionInfos() []sockapi.SessionInfo
}

// Daemon is the running daemon state.
type Daemon struct {
	cfg             *config.Config
	version         string
	manager         SessionManager
	terminalManager *terminal.Manager
	client          *relay.Client
	startedAt       time.Time
	channelRoots    sync.Map
	uploader        *upload.Client
	piBinaryPath    string
	piExtensionPath string
	forcePi         bool
	previewRegistry *preview.Registry
	previewHTTP     *preview.HTTPHandler
	previewWS       *preview.WebSocketBridge
	runCtxMu        sync.RWMutex
	runCtx          context.Context
}

type terminalRelaySender struct {
	client interface {
		Send(context.Context, any) error
	}
}

func (s terminalRelaySender) SendTerminalOpened(req terminal.OpenRequest, shell string, cwd string, startedAt time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.client.Send(ctx, &protocol.TerminalOpened{
		Type:       protocol.MsgTypeTerminalOpened,
		RequestID:  req.RequestID,
		TerminalID: req.TerminalID,
		SessionID:  req.SessionID,
		ChannelID:  req.ChannelID,
		Shell:      shell,
		CWD:        cwd,
		StartedAt:  startedAt.Format(time.RFC3339Nano),
	})
}

func (s terminalRelaySender) SendTerminalOutput(terminalID, sessionID, channelID string, seq int64, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.client.Send(ctx, &protocol.TerminalOutput{
		Type:       protocol.MsgTypeTerminalOutput,
		TerminalID: terminalID,
		SessionID:  sessionID,
		ChannelID:  channelID,
		Seq:        seq,
		DataBase64: terminal.Encode(data),
	})
}

func (s terminalRelaySender) SendTerminalSnapshot(terminalID, sessionID, channelID string, seq int64, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.client.Send(ctx, &protocol.TerminalSnapshot{
		Type:       protocol.MsgTypeTerminalSnapshot,
		TerminalID: terminalID,
		SessionID:  sessionID,
		ChannelID:  channelID,
		Seq:        seq,
		DataBase64: terminal.Encode(data),
	})
}

func (s terminalRelaySender) SendTerminalExit(terminalID, sessionID, channelID, reason string, exitCode int, signal string, endedAt time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.client.Send(ctx, &protocol.TerminalExit{
		Type:       protocol.MsgTypeTerminalExit,
		TerminalID: terminalID,
		SessionID:  sessionID,
		ChannelID:  channelID,
		ExitCode:   &exitCode,
		Signal:     signal,
		Reason:     reason,
		EndedAt:    endedAt.Format(time.RFC3339Nano),
	})
}

func (s terminalRelaySender) SendTerminalError(requestID, terminalID, sessionID, channelID, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.client.Send(ctx, &protocol.TerminalError{
		Type:       protocol.MsgTypeTerminalError,
		RequestID:  requestID,
		TerminalID: terminalID,
		SessionID:  sessionID,
		ChannelID:  channelID,
		Error:      message,
	})
}

// buildRelayURL constructs the WebSocket URL with machineId query param only.
// The auth token is sent exclusively in the Authorization header (relay/client.go)
// and must NOT appear in the URL where it would leak into server logs, proxy logs,
// and HTTP Referer headers.
func buildRelayURL(cfg *config.Config) string {
	u, err := url.Parse(cfg.RelayURL)
	if err != nil {
		return cfg.RelayURL + "?machineId=" + url.QueryEscape(cfg.MachineID)
	}
	q := u.Query()
	q.Set("machineId", cfg.MachineID)
	u.RawQuery = q.Encode()
	return u.String()
}

// New constructs a Daemon that spawns the real `claude` CLI on PATH.
func New(cfg *config.Config, version string) (*Daemon, error) {
	return NewWithBinaryPath(cfg, version, "claude")
}

func defaultPiExtensionPath() string {
	if override := os.Getenv("GSD_PI_EXTENSION_PATH"); override != "" {
		return override
	}
	var installedPath string
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "pi-extension", "index.ts")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
		installedPath = candidate
	}
	repoPath := filepath.Join("internal", "pi", "extension", "index.ts")
	if _, statErr := os.Stat(repoPath); statErr == nil {
		return repoPath
	}
	if installedPath != "" {
		return installedPath
	}
	return repoPath
}

// NewWithBinaryPath constructs a Daemon that spawns the given binary instead
// of the default `claude`. Used by integration tests to inject fake-claude.
func NewWithBinaryPath(cfg *config.Config, version, binaryPath string) (*Daemon, error) {
	client := relay.NewClient(relay.Config{
		URL:           buildRelayURL(cfg),
		AuthToken:     cfg.AuthToken,
		MachineID:     cfg.MachineID,
		DaemonVersion: version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	})

	// Clean up stale PID files from previous crashes
	pidDir, err := pidfile.Dir()
	if err != nil {
		slog.Warn("pid dir unavailable", "err", err)
		pidDir = "" // disable PID tracking if we can't get the dir
	} else {
		if n := pidfile.CleanStale(pidDir); n > 0 {
			slog.Info("cleaned stale pid files", "count", n, "pidDir", pidDir)
		}
	}

	uploader := upload.NewClient(cfg.RelayURL, cfg.MachineID, cfg.AuthToken)
	piBinaryPath := os.Getenv("GSD_PI_BINARY")
	if piBinaryPath == "" {
		piBinaryPath = "pi"
	}
	piExtensionPath := defaultPiExtensionPath()
	// Self-heal pi extension dependencies if missing. Covers daemons that
	// auto-updated through v0.2.31, whose updater shipped source-only
	// tarballs but didn't run npm ci on them. Idempotent: no-op when the
	// extension is already healthy or not installed at all.
	if extDir := filepath.Dir(piExtensionPath); extDir != "" && extDir != "." {
		if err := update.EnsureExtensionHealthy(extDir); err != nil {
			slog.Warn("pi extension self-heal failed; pi-routed tasks will fail until repaired",
				"err", err,
				"hint", "run `gsd-cloud doctor` for diagnostics, or reinstall: curl -fsSL https://install.gsd.build | sh",
			)
		}
	}
	forcePi := os.Getenv("GSD_FORCE_PI") == "1"

	manager := session.NewManager(session.ManagerOptions{
		BinaryPath:      binaryPath,
		PiBinaryPath:    piBinaryPath,
		PiExtensionPath: piExtensionPath,
		Relay:           client,
		Config:          cfg,
		PIDDir:          pidDir,
		Uploader:        uploader,
	})
	previewRegistry := preview.NewRegistry()

	d := &Daemon{
		cfg:             cfg,
		version:         version,
		manager:         manager,
		terminalManager: terminal.NewManager(terminalRelaySender{client: client}, terminal.DefaultLimits()),
		client:          client,
		startedAt:       time.Now(),
		uploader:        uploader,
		piBinaryPath:    piBinaryPath,
		piExtensionPath: piExtensionPath,
		forcePi:         forcePi,
		previewRegistry: previewRegistry,
		previewHTTP:     &preview.HTTPHandler{Registry: previewRegistry, Sender: client},
		previewWS:       preview.NewWebSocketBridge(previewRegistry, client),
	}

	return d, nil
}

// checkAndRefreshToken checks whether the stored token is within 7 days of
// expiry and, if so, calls the cloud refresh-token endpoint to rotate it.
func (d *Daemon) checkAndRefreshToken() {
	if d.cfg.TokenExpiresAt == "" {
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, d.cfg.TokenExpiresAt)
	if err != nil {
		slog.Warn("cannot parse tokenExpiresAt", "error", err)
		return
	}
	if time.Until(expiresAt) > 7*24*time.Hour {
		return
	}

	slog.Info("token expires soon, refreshing")
	client := api.NewClient(d.cfg.ServerURL)
	resp, err := client.RefreshToken(api.RefreshTokenRequest{
		MachineID: d.cfg.MachineID,
		Token:     d.cfg.AuthToken,
	})
	if err != nil {
		slog.Warn("token refresh failed", "error", err)
		return
	}

	d.cfg.AuthToken = resp.AuthToken
	d.cfg.TokenExpiresAt = resp.TokenExpiresAt
	if d.client != nil {
		d.client.SetAuthToken(resp.AuthToken)
	}
	if d.uploader != nil {
		d.uploader.SetAuthToken(resp.AuthToken)
	}
	if err := config.Save(d.cfg); err != nil {
		slog.Warn("failed to save refreshed config", "error", err)
		return
	}
	slog.Info("token refreshed successfully")
}

// runTokenRefreshCheck periodically checks token expiry and refreshes if needed.
func (d *Daemon) runTokenRefreshCheck(ctx context.Context) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkAndRefreshToken()
		}
	}
}

// Health implements sockapi.StatusProvider.
func (d *Daemon) Health() sockapi.HealthData {
	if !d.client.Connected() {
		return sockapi.HealthData{Status: "disconnected"}
	}
	return sockapi.HealthData{Status: "ok"}
}

// Status implements sockapi.StatusProvider.
func (d *Daemon) Status() sockapi.StatusData {
	total, executing := d.manager.ActiveCount()
	return sockapi.StatusData{
		Version:            d.version,
		Uptime:             time.Since(d.startedAt).Truncate(time.Second).String(),
		RelayConnected:     d.client.Connected(),
		RelayURL:           d.cfg.RelayURL,
		MachineID:          d.cfg.MachineID,
		ActiveSessions:     total,
		InFlightTasks:      executing,
		MaxConcurrentTasks: d.cfg.EffectiveMaxConcurrentTasks(),
		LogLevel:           d.cfg.LogLevel,
	}
}

// Sessions implements sockapi.StatusProvider.
func (d *Daemon) Sessions() []sockapi.SessionInfo {
	return d.manager.SessionInfos()
}

// Run connects to the relay and blocks until ctx is canceled.
// The client handles reconnection automatically.
func (d *Daemon) Run(ctx context.Context) error {
	d.runCtxMu.Lock()
	d.runCtx = ctx
	d.runCtxMu.Unlock()
	defer func() {
		d.runCtxMu.Lock()
		d.runCtx = nil
		d.runCtxMu.Unlock()
	}()

	d.client.SetHandler(d.handleMessage)

	// Check token expiry at startup.
	d.checkAndRefreshToken()

	// Start Unix socket status API.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("user home: %w", err)
	}
	sockPath := filepath.Join(home, ".gsd-cloud", "daemon.sock")
	sockSrv := sockapi.NewServer(sockPath, d)
	go func() {
		if err := sockSrv.ListenAndServe(ctx); err != nil {
			slog.Warn("socket API failed", "error", err)
		}
	}()

	go d.runTokenRefreshCheck(ctx)
	go d.runHeartbeat(ctx)
	d.manager.StartReaper(ctx, 5*time.Minute, 30*time.Minute)
	defer d.gracefulShutdown(ctx)

	slog.Info("connecting to relay", "url", d.cfg.RelayURL, "machine", d.cfg.MachineID)

	err = d.client.Run(ctx, d.getActiveTasks)
	if err != nil && strings.Contains(err.Error(), "token_expired") {
		return fmt.Errorf("machine token has expired — run `gsd-cloud login` to re-pair this machine")
	}
	return err
}

// getActiveTasks returns the list of currently executing task IDs.
// Called by the client on every connect/reconnect for Hello state sync.
func (d *Daemon) getActiveTasks() []string {
	return d.manager.ActiveTaskIDs()
}

func (d *Daemon) runHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_ = d.client.Send(sendCtx, &protocol.Heartbeat{
				Type:          protocol.MsgTypeHeartbeat,
				MachineID:     d.cfg.MachineID,
				DaemonVersion: d.version,
				Status:        "online",
				Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
			})
			cancel()
		}
	}
}

func (d *Daemon) handleMessage(env *protocol.Envelope) error {
	slog.Debug("received message", "type", env.Type)
	switch msg := env.Payload.(type) {
	case *protocol.Task:
		return d.handleTask(msg)
	case *protocol.Stop:
		return d.handleStop(msg)
	case *protocol.BrowseDir:
		return d.handleBrowse(msg)
	case *protocol.MkDir:
		return d.handleMkDir(msg)
	case *protocol.ReadFile:
		return d.handleRead(msg)
	case *protocol.PermissionResponse:
		return d.handlePermissionResponse(msg)
	case *protocol.QuestionResponse:
		return d.handleQuestionResponse(msg)
	case *protocol.TerminalOpen:
		return d.handleTerminalOpen(msg)
	case *protocol.TerminalInput:
		return d.handleTerminalInput(msg)
	case *protocol.TerminalResize:
		return d.handleTerminalResize(msg)
	case *protocol.TerminalClose:
		return d.handleTerminalClose(msg)
	case *protocol.CompactRequest:
		return d.handleCompactRequest(msg)
	case *protocol.ContextStatsRequest:
		return d.handleContextStatsRequest(msg)
	case *protocol.PreviewOpen:
		return d.handlePreviewOpen(msg)
	case *protocol.PreviewClose:
		return d.handlePreviewClose(msg)
	case *protocol.PreviewHTTPRequest:
		return d.previewHTTP.Handle(d.runtimeContext(), msg)
	case *protocol.PreviewStreamCancel:
		d.previewRegistry.CancelStream(msg.StreamID)
		return nil
	case *protocol.PreviewWebSocketOpen:
		return d.previewWS.Open(d.runtimeContext(), msg)
	case *protocol.PreviewWebSocketData:
		return d.previewWS.Data(d.runtimeContext(), msg)
	case *protocol.PreviewWebSocketClose:
		return d.previewWS.Close(d.runtimeContext(), msg)
	default:
		// Ignore other types
		return nil
	}
}

func (d *Daemon) runtimeContext() context.Context {
	d.runCtxMu.RLock()
	defer d.runCtxMu.RUnlock()
	if d.runCtx != nil {
		return d.runCtx
	}
	return context.Background()
}

func (d *Daemon) handlePreviewOpen(msg *protocol.PreviewOpen) error {
	target, err := preview.NormalizeTarget(msg.TargetHost, msg.TargetPort)
	if err != nil {
		return d.sendPreviewOpenResult(msg, false, "unsafe_target", err.Error())
	}
	expiresAt, err := time.Parse(time.RFC3339, msg.ExpiresAt)
	if err != nil {
		return d.sendPreviewOpenResult(msg, false, "invalid_request", "invalid preview expiry")
	}
	if err := d.previewRegistry.Open(context.Background(), preview.OpenRequest{
		PreviewID: msg.PreviewID,
		SessionID: msg.SessionID,
		ChannelID: msg.ChannelID,
		MachineID: msg.MachineID,
		Target:    target,
		ExpiresAt: expiresAt,
	}); err != nil {
		return d.sendPreviewOpenResult(msg, false, "open_failed", err.Error())
	}
	return d.sendPreviewOpenResult(msg, true, "", "")
}

func (d *Daemon) handlePreviewClose(msg *protocol.PreviewClose) error {
	d.previewRegistry.Close(msg.PreviewID)
	return nil
}

func (d *Daemon) sendPreviewOpenResult(msg *protocol.PreviewOpen, ok bool, code string, message string) error {
	sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return d.client.Send(sendCtx, &protocol.PreviewOpenResult{
		Type:      protocol.MsgTypePreviewOpenResult,
		RequestID: msg.RequestID,
		PreviewID: msg.PreviewID,
		OK:        ok,
		ErrorCode: code,
		Message:   message,
	})
}

func (d *Daemon) handleTask(msg *protocol.Task) error {
	ctx := context.Background()
	if msg.ChannelID != "" && msg.CWD != "" {
		d.channelRoots.Store(msg.ChannelID, msg.CWD)
	}
	if d.forcePi {
		msg.Engine = "pi"
	}
	actor := d.manager.Get(msg.SessionID)
	if actor != nil && actor.HasTaskID(msg.TaskID) {
		slog.Info("duplicate task ignored", "session", msg.SessionID, "taskId", msg.TaskID)
		return nil
	}
	if actor == nil {
		var err error
		actor, err = d.manager.Spawn(ctx, session.Options{
			SessionID:       msg.SessionID,
			CWD:             msg.CWD,
			Model:           msg.Model,
			Effort:          msg.Effort,
			PermissionMode:  msg.PermissionMode,
			ResumeSession:   msg.ClaudeSessionID,
			PiBinaryPath:    d.piBinaryPath,
			PiExtensionPath: d.piExtensionPath,
		})
		if err != nil {
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return d.client.Send(sendCtx, &protocol.TaskError{
				Type:      protocol.MsgTypeTaskError,
				TaskID:    msg.TaskID,
				SessionID: msg.SessionID,
				ChannelID: msg.ChannelID,
				Error:     err.Error(),
			})
		}
	}

	// Task execution errors (e.g. claude binary not found, executor not ready)
	// must NOT propagate up — that would kill the relay connection and take the
	// entire daemon offline. Report the failure to the browser and keep running.
	if err := actor.SendTask(*msg); err != nil {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return d.client.Send(sendCtx, &protocol.TaskError{
			Type:      protocol.MsgTypeTaskError,
			TaskID:    msg.TaskID,
			SessionID: msg.SessionID,
			ChannelID: msg.ChannelID,
			Error:     err.Error(),
		})
	}
	return nil
}

func (d *Daemon) handleStop(msg *protocol.Stop) error {
	actor := d.manager.Get(msg.SessionID)
	if actor != nil {
		actor.CancelTask()
	}
	return nil
}

func (d *Daemon) handleTerminalOpen(msg *protocol.TerminalOpen) error {
	return d.terminalManager.Open(context.Background(), terminal.OpenRequest{
		RequestID:  msg.RequestID,
		TerminalID: msg.TerminalID,
		SessionID:  msg.SessionID,
		ChannelID:  msg.ChannelID,
		CWD:        msg.CWD,
		Cols:       msg.Cols,
		Rows:       msg.Rows,
	})
}

func (d *Daemon) handleTerminalInput(msg *protocol.TerminalInput) error {
	data, err := base64.StdEncoding.DecodeString(msg.DataBase64)
	if err != nil {
		return nil
	}
	return d.terminalManager.Input(msg.TerminalID, data)
}

func (d *Daemon) handleTerminalResize(msg *protocol.TerminalResize) error {
	return d.terminalManager.Resize(msg.TerminalID, msg.Cols, msg.Rows)
}

func (d *Daemon) handleTerminalClose(msg *protocol.TerminalClose) error {
	d.terminalManager.Close(msg.TerminalID, terminal.ReasonClosedByUser)
	return nil
}

func (d *Daemon) handleCompactRequest(msg *protocol.CompactRequest) error {
	actor := d.manager.Get(msg.SessionID)
	if actor == nil {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return d.client.Send(sendCtx, &protocol.CompactStatus{
			Type:                 protocol.MsgTypeCompactStatus,
			SessionID:            msg.SessionID,
			ChannelID:            msg.ChannelID,
			RequestID:            msg.RequestID,
			Status:               protocol.CompactStatusFailed,
			Reason:               protocol.CompactReasonManual,
			Instructions:         msg.Instructions,
			ContextWindow:        0,
			ReserveTokens:        16384,
			KeepRecentTokens:     20000,
			AutoThresholdPercent: 0,
			Error:                "session is not active on this daemon",
			Source:               "pi",
			ObservedAt:           time.Now().UTC(),
		})
	}
	go actor.HandleCompactRequest(context.Background(), msg)
	return nil
}

func (d *Daemon) handleContextStatsRequest(msg *protocol.ContextStatsRequest) error {
	actor := d.manager.Get(msg.SessionID)
	if actor == nil {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return d.client.Send(sendCtx, &protocol.ContextStats{
			Type:                 protocol.MsgTypeContextStats,
			SessionID:            msg.SessionID,
			ChannelID:            msg.ChannelID,
			RequestID:            msg.RequestID,
			ContextWindow:        0,
			ReserveTokens:        16384,
			KeepRecentTokens:     20000,
			AutoThresholdPercent: 0,
			Source:               "pi",
			ObservedAt:           time.Now().UTC(),
		})
	}
	go actor.HandleContextStatsRequest(context.Background(), msg)
	return nil
}

func (d *Daemon) handleBrowse(msg *protocol.BrowseDir) error {
	entries, err := fs.BrowseDir(msg.Path, d.scopeRootForChannel(msg.ChannelID))
	result := &protocol.BrowseDirResult{
		Type:      protocol.MsgTypeBrowseDirResult,
		RequestID: msg.RequestID,
		ChannelID: msg.ChannelID,
		OK:        err == nil,
		Entries:   entries,
	}
	if err != nil {
		result.Error = err.Error()
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return d.client.Send(sendCtx, result)
}

func (d *Daemon) handleMkDir(msg *protocol.MkDir) error {
	err := fs.MkDir(msg.Path, d.scopeRootForChannel(msg.ChannelID))
	result := &protocol.MkDirResult{
		Type:      protocol.MsgTypeMkDirResult,
		RequestID: msg.RequestID,
		ChannelID: msg.ChannelID,
		OK:        err == nil,
	}
	if err != nil {
		result.Error = err.Error()
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return d.client.Send(sendCtx, result)
}

func (d *Daemon) handleRead(msg *protocol.ReadFile) error {
	content, truncated, err := fs.ReadFile(msg.Path, d.scopeRootForChannel(msg.ChannelID), msg.MaxBytes)
	result := &protocol.ReadFileResult{
		Type:      protocol.MsgTypeReadFileResult,
		RequestID: msg.RequestID,
		ChannelID: msg.ChannelID,
		OK:        err == nil,
		Content:   content,
		Truncated: truncated,
	}
	if err != nil {
		result.Error = err.Error()
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return d.client.Send(sendCtx, result)
}

func (d *Daemon) scopeRootForChannel(channelID string) string {
	if channelID == "" {
		return ""
	}
	root, ok := d.channelRoots.Load(channelID)
	if !ok {
		return ""
	}
	rootStr, _ := root.(string)
	return rootStr
}

func (d *Daemon) handlePermissionResponse(msg *protocol.PermissionResponse) error {
	actor := d.manager.Get(msg.SessionID)
	if actor == nil {
		slog.Warn("no actor for permission response", "session", msg.SessionID)
		return nil
	}
	if err := actor.HandlePermissionResponse(msg); err != nil {
		slog.Warn("permission response failed", "session", msg.SessionID, "error", err)
	}
	return nil
}

func (d *Daemon) handleQuestionResponse(msg *protocol.QuestionResponse) error {
	actor := d.manager.Get(msg.SessionID)
	if actor == nil {
		slog.Warn("no actor for question response", "session", msg.SessionID)
		return nil
	}
	if err := actor.HandleQuestionResponse(msg); err != nil {
		slog.Warn("question response failed", "session", msg.SessionID, "error", err)
	}
	return nil
}

// gracefulShutdown performs a two-stage shutdown:
// 1. Stop accepting new tasks (context is already cancelled).
// 2. Wait up to 30 seconds for in-flight tasks to complete.
// 3. Force-stop any remaining actors.
func (d *Daemon) gracefulShutdown(ctx context.Context) {
	slog.Info("graceful shutdown: draining in-flight tasks", "timeout", "30s")

	// Send "going offline" heartbeat to relay.
	if d.client != nil {
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer sendCancel()
		_ = d.client.Send(sendCtx, &protocol.Heartbeat{
			Type:      protocol.MsgTypeHeartbeat,
			MachineID: d.cfg.MachineID,
			Status:    "offline",
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		})
	}

	// Give actors up to 30 seconds to finish.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer drainCancel()

	done := make(chan struct{})
	go func() {
		d.manager.StopAll()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("graceful shutdown: all actors stopped")
	case <-drainCtx.Done():
		slog.Warn("graceful shutdown: drain timeout exceeded, force-stopping")
		d.manager.StopAll()
	}

	// Close WebSocket cleanly.
	if d.client != nil {
		d.client.Close()
	}
}
