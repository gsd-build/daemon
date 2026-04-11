package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Manager holds a pool of session actors, keyed by sessionID.
type Manager struct {
	mu     sync.Mutex
	actors map[string]*Actor

	relay      RelaySender
	binaryPath string
}

// NewManager constructs a Manager.
func NewManager(binaryPath string, relay RelaySender) *Manager {
	return &Manager{
		actors:     make(map[string]*Actor),
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

	sessionID := opts.SessionID
	go func() {
		err := actor.Run(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		slog.Error("actor exited with error", "session", sessionID, "error", err)
	}()
	return actor, nil
}

// ActiveTaskIDs returns a list of task IDs currently being executed.
func (m *Manager) ActiveTaskIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, a := range m.actors {
		if id := a.InFlightTaskID(); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
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

