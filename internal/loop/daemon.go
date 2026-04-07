// Package loop contains the main daemon event loop: connect to relay,
// dispatch incoming messages to the session manager, run periodic heartbeats.
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gsd-cloud/daemon/internal/config"
	"github.com/gsd-cloud/daemon/internal/fs"
	"github.com/gsd-cloud/daemon/internal/relay"
	"github.com/gsd-cloud/daemon/internal/session"
	"github.com/gsd-cloud/daemon/internal/wal"
	protocol "github.com/gsd-cloud/protocol-go"
)

// Daemon is the running daemon state.
type Daemon struct {
	cfg     *config.Config
	version string
	manager *session.Manager
	client  *relay.Client
	walDir  string
}

// New constructs a Daemon.
func New(cfg *config.Config, version string) (*Daemon, error) {
	home, err := configHomeDir()
	if err != nil {
		return nil, err
	}
	walDir := filepath.Join(home, "wal")

	client := relay.NewClient(relay.Config{
		URL:           cfg.RelayURL + "?token=" + cfg.AuthToken,
		AuthToken:     cfg.AuthToken,
		MachineID:     cfg.MachineID,
		DaemonVersion: version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	})

	manager := session.NewManager(walDir, "claude", client)

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

	// Collect last sequences from local WAL (for now: nothing since no sessions yet)
	lastSeqs := d.manager.LastSequences()

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

func configHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, ".gsd-cloud"), nil
}
