package session

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"

	protocol "github.com/gsd-build/protocol-go"
)

// Manager holds a pool of session actors, keyed by sessionID.
type Manager struct {
	mu     sync.Mutex
	actors map[string]*Actor

	baseWALDir string
	relay      RelaySender
	binaryPath string
}

// NewManager constructs a Manager rooted at baseWALDir.
func NewManager(baseWALDir, binaryPath string, relay RelaySender) *Manager {
	return &Manager{
		actors:     make(map[string]*Actor),
		baseWALDir: baseWALDir,
		relay:      relay,
		binaryPath: binaryPath,
	}
}

// Get returns an existing actor or nil.
func (m *Manager) Get(sessionID string) *Actor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.actors[sessionID]
}

// Spawn creates and starts a new actor for the session.
func (m *Manager) Spawn(
	ctx context.Context,
	opts Options,
) (*Actor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing := m.actors[opts.SessionID]; existing != nil {
		return existing, nil
	}

	if opts.WALPath == "" {
		opts.WALPath = filepath.Join(m.baseWALDir, opts.SessionID+".jsonl")
	}
	if opts.Relay == nil {
		opts.Relay = m.relay
	}
	if opts.BinaryPath == "" {
		opts.BinaryPath = m.binaryPath
	}

	actor, err := NewActor(opts)
	if err != nil {
		return nil, fmt.Errorf("new actor: %w", err)
	}
	m.actors[opts.SessionID] = actor

	// Capture Run's exit reason so a failure during executor setup
	// (e.g. openClaudePTY returning an error) does not disappear
	// silently. Before this change, the goroutine did
	// `_ = actor.Run(ctx)` and any Start error caused the browser to
	// see taskStarted followed by an indefinite hang, because the
	// concurrent SendTask call blocked on <-e.ready forever. The
	// Executor now closes e.ready on every error path, so Send will
	// unblock with a "not started" error, but the Run error itself
	// still needs to be surfaced — otherwise fly logs are silent and
	// the root cause is invisible.
	//
	// Also synthesize a TaskError frame to the relay for any task
	// that happened to be in flight when Run exited, so the browser
	// sees an actionable failure instead of just an empty
	// taskStarted. The relay sender is available via opts.Relay
	// (which Spawn populates from m.relay above), so we do not need
	// to thread new plumbing.
	relay := opts.Relay
	sessionID := opts.SessionID
	channelID := opts.ChannelID
	go func() {
		err := actor.Run(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		log.Printf("[session] actor.Run exited with error: session=%s err=%v", sessionID, err)
		if relay == nil {
			return
		}
		taskID := actor.InFlightTaskID()
		if taskID == "" {
			// No task was in flight; nothing actionable to send.
			return
		}
		if sendErr := relay.Send(&protocol.TaskError{
			Type:      protocol.MsgTypeTaskError,
			TaskID:    taskID,
			SessionID: sessionID,
			ChannelID: channelID,
			Error:     err.Error(),
		}); sendErr != nil {
			log.Printf("[session] failed to send taskError to relay: session=%s err=%v", sessionID, sendErr)
		}
	}()
	return actor, nil
}

// StopAll stops every actor. Called on daemon shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.actors {
		_ = a.Stop()
	}
	m.actors = make(map[string]*Actor)
}

// LastSequences returns a snapshot map of sessionID → lastSeq.
func (m *Manager) LastSequences() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(m.actors))
	for id, a := range m.actors {
		out[id] = a.LastSequence()
	}
	return out
}
