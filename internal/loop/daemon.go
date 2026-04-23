// Package loop contains the main daemon event loop: connect to relay,
// dispatch incoming messages to the session manager, run periodic heartbeats.
package loop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gsd-build/daemon/internal/api"
	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/crons"
	"github.com/gsd-build/daemon/internal/fs"
	"github.com/gsd-build/daemon/internal/pidfile"
	"github.com/gsd-build/daemon/internal/relay"
	"github.com/gsd-build/daemon/internal/session"
	"github.com/gsd-build/daemon/internal/skills"
	"github.com/gsd-build/daemon/internal/sockapi"
	"github.com/gsd-build/daemon/internal/upload"
	"github.com/gsd-build/daemon/internal/workflow"
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
	cfg               *config.Config
	version           string
	manager           SessionManager
	client            *relay.Client
	startedAt         time.Time
	channelRoots      sync.Map
	cronStore         *crons.Store
	cronRuntime       *crons.Runtime
	cronSchedule      *crons.Scheduler
	skillWatcher      *skills.Watcher
	skillPublishMu    sync.Mutex
	skillPublishTimer *time.Timer
	binaryPath        string
	workflowMu        sync.Mutex
	workflows         map[string]*workflow.Executor
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

	manager := session.NewManager(session.ManagerOptions{
		BinaryPath: binaryPath,
		Relay:      client,
		Config:     cfg,
		PIDDir:     pidDir,
		Uploader:   uploader,
	})

	cronDir, err := crons.DefaultDir()
	if err != nil {
		return nil, fmt.Errorf("cron dir: %w", err)
	}
	cronStore := crons.NewStore(cronDir)

	d := &Daemon{
		cfg:        cfg,
		version:    version,
		manager:    manager,
		client:     client,
		startedAt:  time.Now(),
		cronStore:  cronStore,
		binaryPath: binaryPath,
		workflows:  make(map[string]*workflow.Executor),
	}
	d.cronRuntime = crons.NewRuntime(
		cfg.MachineID,
		func() string { return d.cfg.AuthToken },
		api.NewClient(cfg.ServerURL),
		cronStore,
		func(task *protocol.Task) error { return d.handleTask(task) },
		slog.Default(),
	)
	d.cronSchedule = crons.NewScheduler(
		cronStore,
		d.handleCronDue,
		30*time.Second,
		slog.Default(),
	)

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
	d.client.SetHandler(d.handleMessage)
	d.client.SetOnConnect(d.handleRelayConnect)

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
	d.startSkillWatcher(ctx)
	if d.cronSchedule != nil {
		go d.cronSchedule.Run(ctx)
	}
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
	case *protocol.SyncCrons:
		return d.handleSyncCrons(msg)
	case *protocol.PermissionResponse:
		return d.handlePermissionResponse(msg)
	case *protocol.QuestionResponse:
		return d.handleQuestionResponse(msg)
	case *protocol.SkillContentRequest:
		return d.handleSkillContentRequest(msg)
	case *protocol.SkillPush:
		return d.handleSkillPush(msg)
	case *protocol.SkillDelete:
		return d.handleSkillDelete(msg)
	case *protocol.WorkflowRun:
		return d.handleWorkflowRun(msg)
	case *protocol.WorkflowStop:
		return d.handleWorkflowStop(msg)
	case *protocol.WorkflowDesignChat:
		return d.handleWorkflowDesignChat(msg)
	default:
		// Ignore other types
		return nil
	}
}

func (d *Daemon) claudeBinary() string {
	return d.binaryPath
}

func (d *Daemon) handleWorkflowRun(msg *protocol.WorkflowRun) error {
	def, err := workflow.ParseDefinition(msg.Definition)
	if err != nil {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return d.client.Send(sendCtx, &protocol.WorkflowError{
			Type:          protocol.MsgTypeWorkflowError,
			WorkflowRunID: msg.WorkflowRunID,
			ChannelID:     msg.ChannelID,
			Error:         fmt.Sprintf("invalid workflow definition: %v", err),
		})
	}

	cwd := msg.CWD
	if def.WorkingDir != "" {
		cwd = def.WorkingDir
	}
	if msg.ChannelID != "" && cwd != "" {
		d.channelRoots.Store(msg.ChannelID, cwd)
		d.addProjectRootsToSkillWatcher(cwd)
	}

	exec := workflow.NewExecutor(def, cwd, msg.ChannelID, msg.WorkflowRunID, d.client, d.claudeBinary())

	d.workflowMu.Lock()
	d.workflows[msg.WorkflowRunID] = exec
	d.workflowMu.Unlock()

	go func() {
		exec.Run(context.Background())
		d.workflowMu.Lock()
		delete(d.workflows, msg.WorkflowRunID)
		d.workflowMu.Unlock()
	}()

	return nil
}

func (d *Daemon) handleWorkflowStop(msg *protocol.WorkflowStop) error {
	d.workflowMu.Lock()
	exec, ok := d.workflows[msg.WorkflowRunID]
	d.workflowMu.Unlock()
	if ok {
		exec.Stop()
	}
	return nil
}

func (d *Daemon) handleWorkflowDesignChat(msg *protocol.WorkflowDesignChat) error {
	go d.runDesignChat(msg)
	return nil
}

func (d *Daemon) runDesignChat(msg *protocol.WorkflowDesignChat) {
	ctx := context.Background()
	systemPrompt := `You are a workflow design assistant. The user will describe changes to a workflow graph.
You must respond with a JSON array of patch operations that modify the workflow definition.
Each patch follows JSON Patch (RFC 6902) format: {"op": "add|remove|replace", "path": "...", "value": ...}
The workflow structure has "nodes" (array) and "edges" (array). Respond ONLY with the JSON patch array.`

	prompt := fmt.Sprintf("Current workflow:\n%s\n\nUser request: %s", string(msg.Definition), msg.UserText)

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--model", "sonnet",
		"--append-system-prompt", systemPrompt,
		"--", prompt,
	}

	cmd := exec.CommandContext(ctx, d.claudeBinary(), args...)
	cmd.Dir = msg.CWD

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		slog.Error("workflow design chat: stdout pipe", "err", err)
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Error("workflow design chat: start", "err", err)
		return
	}

	scanner := workflow.NewNDJSONScanner(stdout)
	for scanner.Scan() {
		event := scanner.Event()
		sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := d.client.Send(sendCtx, &protocol.WorkflowDesignChatStream{
			Type:       protocol.MsgTypeWorkflowDesignChatStream,
			WorkflowID: msg.WorkflowID,
			ChannelID:  msg.ChannelID,
			Event:      event.Raw,
		}); err != nil {
			slog.Warn("workflow design chat: stream send failed", "err", err)
		}
		cancel()
	}

	if err := cmd.Wait(); err != nil {
		slog.Warn("workflow design chat: claude exited", "err", err)
	}

	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.client.Send(sendCtx, &protocol.WorkflowDesignChatComplete{
		Type:       protocol.MsgTypeWorkflowDesignChatComplete,
		WorkflowID: msg.WorkflowID,
		ChannelID:  msg.ChannelID,
		Patches:    json.RawMessage(`[]`),
	}); err != nil {
		slog.Warn("workflow design chat: complete send failed", "err", err)
	}
}

func (d *Daemon) handleTask(msg *protocol.Task) error {
	ctx := context.Background()
	if msg.ChannelID != "" && msg.CWD != "" {
		projectRootAdded := !d.hasProjectRoot(msg.CWD)
		d.channelRoots.Store(msg.ChannelID, msg.CWD)
		d.addProjectRootsToSkillWatcher(msg.CWD)
		if projectRootAdded {
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := d.sendSkillInventory(sendCtx); err != nil {
				slog.Warn("skill inventory publish failed", "error", err, "projectRoot", msg.CWD)
			}
		}
	}
	actor := d.manager.Get(msg.SessionID)
	if actor != nil && actor.HasTaskID(msg.TaskID) {
		slog.Info("duplicate task ignored", "session", msg.SessionID, "taskId", msg.TaskID)
		return nil
	}
	if actor == nil {
		var err error
		actor, err = d.manager.Spawn(ctx, session.Options{
			SessionID:      msg.SessionID,
			CWD:            msg.CWD,
			Model:          msg.Model,
			Effort:         msg.Effort,
			PermissionMode: msg.PermissionMode,
			SystemPrompt:   msg.PersonaSystemPrompt,
			ResumeSession:  msg.ClaudeSessionID,
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

func (d *Daemon) handleSyncCrons(msg *protocol.SyncCrons) error {
	if d.cronStore == nil {
		return nil
	}
	sentAt, err := time.Parse(time.RFC3339Nano, msg.SentAt)
	if err != nil {
		sentAt = time.Now().UTC()
	}
	if err := d.cronStore.Sync(msg.Jobs, sentAt); err != nil {
		return err
	}
	return d.sendCronInventory()
}

func (d *Daemon) handleStop(msg *protocol.Stop) error {
	actor := d.manager.Get(msg.SessionID)
	if actor != nil {
		actor.CancelTask()
	}
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

func (d *Daemon) handleCronDue(spec protocol.CronSpec, scheduledFor time.Time) error {
	if d.cronRuntime == nil {
		return nil
	}
	if err := d.cronRuntime.HandleDue(spec, scheduledFor); err != nil {
		return err
	}
	return d.sendCronInventory()
}

func (d *Daemon) sendCronInventory() error {
	if d.cronStore == nil {
		return nil
	}
	locals, err := d.cronStore.List()
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return d.client.Send(sendCtx, &protocol.CronInventory{
		Type:      protocol.MsgTypeCronInventory,
		MachineID: d.cfg.MachineID,
		Items:     crons.BuildInventory(locals, time.Now().UTC()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
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

func (d *Daemon) handleRelayConnect(ctx context.Context) error {
	return d.sendSkillInventory(ctx)
}

func (d *Daemon) handleSkillContentRequest(msg *protocol.SkillContentRequest) error {
	managedRoots, err := daemonManagedRoots()
	if err != nil {
		return err
	}
	targetPath := filepath.Join(msg.Root, filepath.FromSlash(msg.RelativePath))
	if _, _, err := fs.ValidateManagedPath(targetPath, managedRoots); err != nil {
		return err
	}
	content, _, err := fs.ReadFile(targetPath, msg.Root, fs.DefaultMaxBytes)
	if err != nil {
		return err
	}

	sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return d.client.Send(sendCtx, &protocol.SkillContentUpload{
		Type:               protocol.MsgTypeSkillContentUpload,
		MachineID:          d.cfg.MachineID,
		Slug:               msg.Slug,
		Root:               msg.Root,
		RelativePath:       msg.RelativePath,
		Content:            content,
		MachineFingerprint: skillsFingerprint([]byte(content)),
		BaseCloudRevision:  0,
	})
}

func (d *Daemon) handleSkillPush(msg *protocol.SkillPush) error {
	managedRoots, err := daemonManagedRoots()
	if err != nil {
		return err
	}
	targetPath := filepath.Join(msg.Root, filepath.FromSlash(msg.RelativePath))
	if d.skillWatcher != nil {
		d.skillWatcher.SuppressPath(msg.Root, 500*time.Millisecond)
		d.skillWatcher.SuppressPath(filepath.Dir(targetPath), 500*time.Millisecond)
		d.skillWatcher.SuppressPath(targetPath, 500*time.Millisecond)
	}
	if err := fs.WriteManagedFile(targetPath, managedRoots, []byte(msg.Content)); err != nil {
		return err
	}
	d.scheduleSkillInventoryPublish()
	return nil
}

func (d *Daemon) handleSkillDelete(msg *protocol.SkillDelete) error {
	managedRoots, err := daemonManagedRoots()
	if err != nil {
		return err
	}
	targetPath := filepath.Join(msg.Root, filepath.FromSlash(msg.RelativePath))
	if d.skillWatcher != nil {
		d.skillWatcher.SuppressPath(msg.Root, 500*time.Millisecond)
		d.skillWatcher.SuppressPath(filepath.Dir(targetPath), 500*time.Millisecond)
		d.skillWatcher.SuppressPath(targetPath, 500*time.Millisecond)
	}
	if err := fs.RemoveManagedPath(targetPath, managedRoots); err != nil {
		return err
	}
	d.scheduleSkillInventoryPublish()
	return nil
}

func (d *Daemon) scheduleSkillInventoryPublish() {
	d.skillPublishMu.Lock()
	defer d.skillPublishMu.Unlock()
	if d.skillPublishTimer != nil {
		d.skillPublishTimer.Stop()
	}
	d.skillPublishTimer = time.AfterFunc(75*time.Millisecond, func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := d.sendSkillInventory(sendCtx); err != nil {
			slog.Warn("skill inventory publish failed", "error", err)
		}
	})
}

func (d *Daemon) sendSkillInventory(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	entries, err := skills.Discover(skills.DiscoverOptions{
		HomeDir:      home,
		ProjectRoots: d.projectRoots(),
	})
	if err != nil {
		return err
	}

	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return d.client.Send(sendCtx, &protocol.SkillInventory{
		Type:      protocol.MsgTypeSkillInventory,
		MachineID: d.cfg.MachineID,
		Entries:   entries,
	})
}

func (d *Daemon) startSkillWatcher(ctx context.Context) {
	if d.skillWatcher != nil {
		return
	}
	watcher, err := skills.NewWatcher(skills.WatchOptions{
		Debounce: 250 * time.Millisecond,
		OnChange: func() {
			if d.client == nil || !d.client.Connected() {
				return
			}
			sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := d.sendSkillInventory(sendCtx); err != nil {
				slog.Warn("skill inventory publish failed", "error", err)
			}
		},
	})
	if err != nil {
		slog.Warn("skill watcher unavailable", "error", err)
		return
	}

	managedRoots, err := daemonManagedRoots()
	if err != nil {
		slog.Warn("skill watcher roots unavailable", "error", err)
		return
	}
	for _, root := range managedRoots {
		_ = watcher.AddRoot(root)
	}
	for _, projectRoot := range d.projectRoots() {
		for _, root := range skills.ProjectSkillRoots(projectRoot) {
			_ = watcher.AddRoot(root)
		}
	}
	d.skillWatcher = watcher
	go func() {
		if err := watcher.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("skill watcher stopped", "error", err)
		}
	}()
}

func (d *Daemon) addProjectRootsToSkillWatcher(projectRoot string) {
	if d.skillWatcher == nil || projectRoot == "" {
		return
	}
	for _, root := range skills.ProjectSkillRoots(projectRoot) {
		_ = d.skillWatcher.AddRoot(root)
	}
}

func (d *Daemon) projectRoots() []string {
	seen := map[string]struct{}{}
	var roots []string
	d.channelRoots.Range(func(_, value any) bool {
		root, _ := value.(string)
		if root == "" {
			return true
		}
		if _, ok := seen[root]; ok {
			return true
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
		return true
	})
	return roots
}

func (d *Daemon) hasProjectRoot(projectRoot string) bool {
	for _, root := range d.projectRoots() {
		if root == projectRoot {
			return true
		}
	}
	return false
}

func daemonManagedRoots() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return skills.ManagedRoots(home), nil
}

func skillsFingerprint(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("%x", sum[:])
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
