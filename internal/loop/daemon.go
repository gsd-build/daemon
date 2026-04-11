// Package loop contains the main daemon event loop: connect to relay,
// dispatch incoming messages to the session manager, run periodic heartbeats.
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"strings"

	"github.com/gsd-build/daemon/internal/api"
	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/display"
	"github.com/gsd-build/daemon/internal/fs"
	"github.com/gsd-build/daemon/internal/relay"
	"github.com/gsd-build/daemon/internal/session"
	"github.com/gsd-build/daemon/internal/wal"
	protocol "github.com/gsd-build/protocol-go"
)

// Daemon is the running daemon state.
type Daemon struct {
	cfg       *config.Config
	version   string
	manager   *session.Manager
	client    *relay.Client
	walDir    string
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
	home, err := configHomeDir()
	if err != nil {
		return nil, err
	}
	walDir := filepath.Join(home, "wal")

	client := relay.NewClient(relay.Config{
		URL:           buildRelayURL(cfg),
		AuthToken:     cfg.AuthToken,
		MachineID:     cfg.MachineID,
		DaemonVersion: version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	})

	manager := session.NewManager(walDir, binaryPath, client, verbosity)

	return &Daemon{
		cfg:       cfg,
		version:   version,
		manager:   manager,
		client:    client,
		walDir:    walDir,
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
// Automatically reconnects with exponential backoff on connection failures.
func (d *Daemon) Run(ctx context.Context) error {
	d.client.SetHandler(d.handleMessage)

	// Check token expiry at startup.
	d.checkAndRefreshToken()

	backoff := 1 * time.Second
	const maxBackoff = 60 * time.Second

	for {
		connStart := time.Now()
		err := d.runOnce(ctx)
		if err == nil || ctx.Err() != nil {
			return err
		}

		// If the relay rejected us with token_expired, exit immediately.
		if strings.Contains(err.Error(), "token_expired") {
			return fmt.Errorf("machine token has expired — run `gsd-cloud login` to re-pair this machine")
		}

		// Reset backoff if the connection was alive for a while (healthy session).
		if time.Since(connStart) > 2*time.Minute {
			backoff = 1 * time.Second
		}

		fmt.Printf("%srelay disconnected (%s) — %v%s\n", display.Dim, time.Since(connStart).Truncate(time.Second), err, display.Reset)
		fmt.Printf("%sreconnecting in %s...%s\n", display.Dim, backoff, display.Reset)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff = backoff * 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runOnce performs a single connect → run cycle.
func (d *Daemon) runOnce(ctx context.Context) error {
	lastSeqs, err := wal.ScanDirectory(d.walDir)
	if err != nil {
		return fmt.Errorf("scan wal directory: %w", err)
	}

	welcome, err := d.client.Connect(ctx, lastSeqs)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	_ = welcome // TODO: use acked sequences to drive WAL replay

	fmt.Printf("%srelay connected%s\n", display.Dim, display.Reset)

	// Scope heartbeat and token refresh to this connection; cancel when Run returns.
	connCtx, connCancel := context.WithCancel(ctx)
	go d.runHeartbeat(connCtx)
	go d.runIdleHeartbeat(connCtx)
	go d.runTokenRefreshCheck(connCtx)

	err = d.client.Run(ctx)
	connCancel()
	return err
}

func (d *Daemon) runHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = d.client.Send(&protocol.Heartbeat{
				Type:          protocol.MsgTypeHeartbeat,
				MachineID:     d.cfg.MachineID,
				DaemonVersion: d.version,
				Status:        "online",
				Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
			})
		}
	}
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
	case *protocol.Ack:
		return d.handleAck(msg)
	case *protocol.ReplayRequest:
		return d.handleReplay(msg)
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
			return d.client.Send(&protocol.TaskError{
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
		return d.client.Send(&protocol.TaskError{
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
		if err := actor.Stop(); err != nil {
			fmt.Printf("warning: stop session %s: %v\n", msg.SessionID, err)
		}
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
	return d.client.Send(result)
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
	return d.client.Send(result)
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
	return d.client.Send(result)
}

func (d *Daemon) handleAck(msg *protocol.Ack) error {
	actor := d.manager.Get(msg.SessionID)
	if actor != nil {
		if err := actor.PruneWAL(msg.SequenceNumber); err != nil {
			fmt.Printf("warning: prune WAL session %s seq %d: %v\n", msg.SessionID, msg.SequenceNumber, err)
		}
	}
	return nil
}

func (d *Daemon) handleReplay(msg *protocol.ReplayRequest) error {
	// Read the WAL for this session and resend all entries with seq > fromSequence
	walPath := filepath.Join(d.walDir, msg.SessionID+".jsonl")
	walLog, err := wal.Open(walPath)
	if err != nil {
		fmt.Printf("warning: replay open wal session %s: %v\n", msg.SessionID, err)
		return nil
	}
	defer walLog.Close()

	entries, err := walLog.ReadFrom(msg.FromSequence)
	if err != nil {
		fmt.Printf("warning: replay read wal session %s: %v\n", msg.SessionID, err)
		return nil
	}

	for _, e := range entries {
		if err := d.client.Send(json.RawMessage(e.Data)); err != nil {
			fmt.Printf("warning: replay send session %s: %v\n", msg.SessionID, err)
			return nil
		}
	}
	return nil
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

func configHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".gsd-cloud"), nil
}
