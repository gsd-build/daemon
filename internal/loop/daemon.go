// Package loop contains the main daemon event loop: connect to relay,
// dispatch incoming messages to the session manager, run periodic heartbeats.
package loop

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/gsd-build/daemon/internal/api"
	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/display"
	"github.com/gsd-build/daemon/internal/fs"
	"github.com/gsd-build/daemon/internal/relay"
	"github.com/gsd-build/daemon/internal/session"
	protocol "github.com/gsd-build/protocol-go"
)

// Daemon is the running daemon state.
type Daemon struct {
	cfg       *config.Config
	version   string
	manager   *session.Manager
	client    *relay.Client
	verbosity display.VerbosityLevel
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
func New(cfg *config.Config, version string, verbosity display.VerbosityLevel) (*Daemon, error) {
	return NewWithBinaryPath(cfg, version, "claude", verbosity)
}

// NewWithBinaryPath constructs a Daemon that spawns the given binary instead
// of the default `claude`. Used by integration tests to inject fake-claude.
func NewWithBinaryPath(cfg *config.Config, version, binaryPath string, verbosity display.VerbosityLevel) (*Daemon, error) {
	client := relay.NewClient(relay.Config{
		URL:           buildRelayURL(cfg),
		AuthToken:     cfg.AuthToken,
		MachineID:     cfg.MachineID,
		DaemonVersion: version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	})

	manager := session.NewManager(binaryPath, client, verbosity)

	return &Daemon{
		cfg:       cfg,
		version:   version,
		manager:   manager,
		client:    client,
		verbosity: verbosity,
	}, nil
}

// checkAndRefreshToken checks whether the stored token is within 7 days of
// expiry and, if so, calls the cloud refresh-token endpoint to rotate it.
func (d *Daemon) checkAndRefreshToken() {
	if d.cfg.TokenExpiresAt == "" {
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, d.cfg.TokenExpiresAt)
	if err != nil {
		fmt.Printf("warning: cannot parse tokenExpiresAt: %v\n", err)
		return
	}
	if time.Until(expiresAt) > 7*24*time.Hour {
		return
	}

	fmt.Println("token expires soon, refreshing...")
	client := api.NewClient(d.cfg.ServerURL)
	resp, err := client.RefreshToken(api.RefreshTokenRequest{
		MachineID: d.cfg.MachineID,
		Token:     d.cfg.AuthToken,
	})
	if err != nil {
		fmt.Printf("warning: token refresh failed: %v\n", err)
		return
	}

	d.cfg.AuthToken = resp.AuthToken
	d.cfg.TokenExpiresAt = resp.TokenExpiresAt
	if err := config.Save(d.cfg); err != nil {
		fmt.Printf("warning: failed to save refreshed config: %v\n", err)
		return
	}
	fmt.Println("token refreshed successfully")
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

// Run connects to the relay and blocks until ctx is canceled.
// The client handles reconnection automatically.
func (d *Daemon) Run(ctx context.Context) error {
	d.client.SetHandler(d.handleMessage)

	// Check token expiry at startup.
	d.checkAndRefreshToken()
	go d.runTokenRefreshCheck(ctx)
	go d.runIdleHeartbeat(ctx)

	fmt.Printf("Connecting to %s as %s...\n", d.cfg.RelayURL, d.cfg.MachineID)

	err := d.client.Run(ctx, d.getActiveTasks)
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

func (d *Daemon) runIdleHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().Format("15:04")
			fmt.Printf("%s%s ♥ connected · idle%s\n", display.Dim, now, display.Reset)
		}
	}
}

func (d *Daemon) handleMessage(env *protocol.Envelope) error {
	if d.verbosity == display.Debug {
		fmt.Printf("%s[debug] received message type=%s%s\n", display.Dim, env.Type, display.Reset)
	}
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
	default:
		// Ignore other types
		return nil
	}
}

func (d *Daemon) handleTask(msg *protocol.Task) error {
	ctx := context.Background()
	actor := d.manager.Get(msg.SessionID)
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

func (d *Daemon) handleStop(msg *protocol.Stop) error {
	actor := d.manager.Get(msg.SessionID)
	if actor != nil {
		actor.CancelTask()
	}
	return nil
}

func (d *Daemon) handleBrowse(msg *protocol.BrowseDir) error {
	entries, err := fs.BrowseDir(msg.Path)
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
	err := fs.MkDir(msg.Path)
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
	content, truncated, err := fs.ReadFile(msg.Path, msg.MaxBytes)
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

func (d *Daemon) handlePermissionResponse(msg *protocol.PermissionResponse) error {
	actor := d.manager.Get(msg.SessionID)
	if actor == nil {
		fmt.Printf("warning: no actor for permission response session %s\n", msg.SessionID)
		return nil
	}
	if err := actor.HandlePermissionResponse(msg); err != nil {
		fmt.Printf("warning: permission response session %s: %v\n", msg.SessionID, err)
	}
	return nil
}

func (d *Daemon) handleQuestionResponse(msg *protocol.QuestionResponse) error {
	actor := d.manager.Get(msg.SessionID)
	if actor == nil {
		fmt.Printf("warning: no actor for question response session %s\n", msg.SessionID)
		return nil
	}
	if err := actor.HandleQuestionResponse(msg); err != nil {
		fmt.Printf("warning: question response session %s: %v\n", msg.SessionID, err)
	}
	return nil
}

