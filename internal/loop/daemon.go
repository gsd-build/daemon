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

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/fs"
	"github.com/gsd-build/daemon/internal/relay"
	"github.com/gsd-build/daemon/internal/session"
	"github.com/gsd-build/daemon/internal/wal"
	protocol "github.com/gsd-build/protocol-go"
)

// Daemon is the running daemon state.
type Daemon struct {
	cfg     *config.Config
	version string
	manager *session.Manager
	client  *relay.Client
	walDir  string
}

// buildRelayURL constructs the WebSocket URL with machineId + token query params.
// Both must be URL-escaped because tokens may contain "/" or "+" characters.
// Preserves any existing query params already present on cfg.RelayURL.
func buildRelayURL(cfg *config.Config) string {
	u, err := url.Parse(cfg.RelayURL)
	if err != nil {
		// cfg.RelayURL is validated at load time; if parsing fails here,
		// fall back to raw concat so callers get a visible failure rather
		// than a silent wrong URL.
		return cfg.RelayURL + "?machineId=" + url.QueryEscape(cfg.MachineID) + "&token=" + url.QueryEscape(cfg.AuthToken)
	}
	q := u.Query()
	q.Set("machineId", cfg.MachineID)
	q.Set("token", cfg.AuthToken)
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

	manager := session.NewManager(walDir, binaryPath, client)

	return &Daemon{
		cfg:     cfg,
		version: version,
		manager: manager,
		client:  client,
		walDir:  walDir,
	}, nil
}

// Run connects to the relay and blocks until ctx is canceled.
func (d *Daemon) Run(ctx context.Context) error {
	d.client.SetHandler(d.handleMessage)

	// Scan WAL directory to recover high-water marks per session from disk.
	// On cold start the actor pool is empty, so we read sequence numbers
	// directly from persisted WAL files rather than in-memory actor state.
	lastSeqs, err := wal.ScanDirectory(d.walDir)
	if err != nil {
		return fmt.Errorf("scan wal directory: %w", err)
	}

	welcome, err := d.client.Connect(ctx, lastSeqs)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	_ = welcome // TODO: use acked sequences to drive WAL replay

	// Start heartbeat
	go d.runHeartbeat(ctx)

	return d.client.Run(ctx)
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

func (d *Daemon) handleMessage(env *protocol.Envelope) error {
	switch msg := env.Payload.(type) {
	case *protocol.Task:
		return d.handleTask(msg)
	case *protocol.Stop:
		return d.handleStop(msg)
	case *protocol.BrowseDir:
		return d.handleBrowse(msg)
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
			ChannelID:      msg.ChannelID,
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
	return actor.SendTask(*msg)
}

func (d *Daemon) handleStop(msg *protocol.Stop) error {
	actor := d.manager.Get(msg.SessionID)
	if actor != nil {
		return actor.Stop()
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
		return actor.PruneWAL(msg.SequenceNumber)
	}
	return nil
}

func (d *Daemon) handleReplay(msg *protocol.ReplayRequest) error {
	// Read the WAL for this session and resend all entries with seq > fromSequence
	walPath := filepath.Join(d.walDir, msg.SessionID+".jsonl")
	log, err := wal.Open(walPath)
	if err != nil {
		return fmt.Errorf("open wal: %w", err)
	}
	defer log.Close()

	entries, err := log.ReadFrom(msg.FromSequence)
	if err != nil {
		return fmt.Errorf("read wal: %w", err)
	}

	for _, e := range entries {
		// Each entry is a serialized Stream frame; send it back as-is
		if err := d.client.Send(json.RawMessage(e.Data)); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) handlePermissionResponse(msg *protocol.PermissionResponse) error {
	actor := d.manager.Get(msg.SessionID)
	if actor == nil {
		return fmt.Errorf("no actor for session %s", msg.SessionID)
	}
	return actor.HandlePermissionResponse(msg)
}

func (d *Daemon) handleQuestionResponse(msg *protocol.QuestionResponse) error {
	actor := d.manager.Get(msg.SessionID)
	if actor == nil {
		return fmt.Errorf("no actor for session %s", msg.SessionID)
	}
	return actor.HandleQuestionResponse(msg)
}

func configHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".gsd-cloud"), nil
}
