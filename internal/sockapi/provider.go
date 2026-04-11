package sockapi

import "time"

// SessionInfo is a snapshot of one active session.
type SessionInfo struct {
	SessionID string     `json:"sessionID"`
	State     string     `json:"state"`     // "executing" or "idle"
	TaskID    string     `json:"taskID"`    // empty when idle
	StartedAt *time.Time `json:"startedAt"` // when current task started; nil when idle
	IdleSince *time.Time `json:"idleSince"` // when actor became idle; nil when executing
}

// StatusData is the full daemon status snapshot.
type StatusData struct {
	Version            string `json:"version"`
	Uptime             string `json:"uptime"`
	RelayConnected     bool   `json:"relayConnected"`
	RelayURL           string `json:"relayURL"`
	MachineID          string `json:"machineID"`
	ActiveSessions     int    `json:"activeSessions"`
	InFlightTasks      int    `json:"inFlightTasks"`
	MaxConcurrentTasks int    `json:"maxConcurrentTasks"`
	LogLevel           string `json:"logLevel"`
}

// HealthData is the health check response.
type HealthData struct {
	Status string `json:"status"` // "ok" or "disconnected"
}

// StatusProvider is implemented by the daemon loop to expose live state.
type StatusProvider interface {
	Health() HealthData
	Status() StatusData
	Sessions() []SessionInfo
}
