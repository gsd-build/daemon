package session

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/sockapi"
)

// ManagerOptions configures a new Manager.
type ManagerOptions struct {
	BinaryPath string
	Relay      RelaySender
	Config     *config.Config
	PIDDir     string         // directory for child PID files; empty disables
	Uploader   ImageUploader  // nil = image upload disabled
}

// Manager holds a pool of session actors, keyed by sessionID.
type Manager struct {
	mu     sync.Mutex
	actors map[string]*Actor

	relay      RelaySender
	binaryPath string
	cfg        *config.Config
	pidDir     string
	uploader   ImageUploader
}

// NewManager constructs a Manager.
func NewManager(opts ManagerOptions) *Manager {
	return &Manager{
		actors:     make(map[string]*Actor),
		relay:      opts.Relay,
		binaryPath: opts.BinaryPath,
		cfg:        opts.Config,
		pidDir:     opts.PIDDir,
		uploader:   opts.Uploader,
	}
}

// Get returns an existing actor or nil.
func (m *Manager) Get(sessionID string) *Actor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.actors[sessionID]
}

// InFlightCount returns the number of actors with in-flight tasks.
func (m *Manager) InFlightCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, a := range m.actors {
		if a.HasInFlightTask() {
			count++
		}
	}
	return count
}

// ActiveCount returns the total number of actors and how many are executing.
func (m *Manager) ActiveCount() (total int, executing int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	total = len(m.actors)
	for _, a := range m.actors {
		if a.HasInFlightTask() {
			executing++
		}
	}
	return total, executing
}

// Spawn creates and starts a new actor for the session.
// Returns an existing actor if one already exists for the session.
// Returns an error if the machine is at capacity.
func (m *Manager) Spawn(
	ctx context.Context,
	opts Options,
) (*Actor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing := m.actors[opts.SessionID]; existing != nil {
		return existing, nil
	}

	// Concurrency check: count actors that are executing or pending.
	// A freshly spawned actor has taskID="" AND idleSince==nil (never
	// completed a task yet). We must count these as "busy" because they
	// have a task queued via SendTask that hasn't been picked up by
	// executeTask yet. Without this, rapid task delivery bypasses the
	// limit — all actors appear idle during the race window.
	maxTasks := m.cfg.EffectiveMaxConcurrentTasks()
	busy := 0
	for _, a := range m.actors {
		if a.HasInFlightTask() || !a.HasBeenIdle() {
			busy++
		}
	}
	if busy >= maxTasks {
		return nil, fmt.Errorf("machine at capacity — %d/%d tasks running, try again shortly", busy, maxTasks)
	}

	// Memory safety net: reject if available memory < 10% of total
	if memoryTooLow() {
		return nil, fmt.Errorf("machine at capacity — available memory below 10%%, try again shortly")
	}

	if opts.Relay == nil {
		opts.Relay = m.relay
	}
	if opts.BinaryPath == "" {
		opts.BinaryPath = m.binaryPath
	}
	if opts.Uploader == nil {
		opts.Uploader = m.uploader
	}

	actor, err := NewActor(opts)
	if err != nil {
		return nil, fmt.Errorf("new actor: %w", err)
	}
	actor.taskTimeout = m.cfg.EffectiveTaskTimeout()
	actor.pidDir = m.pidDir
	m.actors[opts.SessionID] = actor

	sessionID := opts.SessionID
	go func() {
		err := actor.Run(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		log.Printf("[session] actor.Run exited with error: session=%s err=%v", sessionID, err)
	}()
	return actor, nil
}

// Remove removes an actor from the map. Called by the reaper.
func (m *Manager) Remove(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.actors[sessionID]; ok {
		_ = a.Stop()
		delete(m.actors, sessionID)
	}
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

// SessionInfos returns a snapshot of all active sessions.
func (m *Manager) SessionInfos() []sockapi.SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	infos := make([]sockapi.SessionInfo, 0, len(m.actors))
	for _, a := range m.actors {
		infos = append(infos, a.Info())
	}
	return infos
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

// memoryTooLow returns true if available system memory is below 10% of total.
func memoryTooLow() bool {
	total, avail, err := systemMemory()
	if err != nil || total == 0 {
		return false // can't determine — don't block tasks
	}
	return float64(avail) < float64(total)*0.10
}

// ReapIdleActors stops and removes actors idle longer than maxIdle.
// Actors with in-flight tasks are never reaped. Returns the count of reaped actors.
func (m *Manager) ReapIdleActors(maxIdle time.Duration) int {
	m.mu.Lock()
	var toReap []string
	cutoff := time.Now().Add(-maxIdle)
	for id, a := range m.actors {
		if a.HasInFlightTask() {
			continue
		}
		if a.LastActiveAt().Before(cutoff) {
			toReap = append(toReap, id)
		}
	}
	m.mu.Unlock()

	for _, id := range toReap {
		m.Remove(id)
		log.Printf("[reaper] reaped idle actor: session=%s", id)
	}
	return len(toReap)
}

// StartReaper launches a goroutine that reaps idle actors on a tick interval.
// Runs until ctx is cancelled.
func (m *Manager) StartReaper(ctx context.Context, tick time.Duration, maxIdle time.Duration) {
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := m.ReapIdleActors(maxIdle); n > 0 {
					log.Printf("[reaper] reaped %d idle actor(s)", n)
				}
			}
		}
	}()
}
