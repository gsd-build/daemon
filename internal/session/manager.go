package session

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
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

	go func() {
		_ = actor.Run(ctx)
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
