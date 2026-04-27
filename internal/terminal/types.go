package terminal

import "time"

const (
	ReasonProcessExit       = "process_exit"
	ReasonClosedByUser      = "closed_by_user"
	ReasonDisconnectTimeout = "disconnect_timeout"
	ReasonMaxLifetime       = "max_lifetime"
	ReasonDaemonShutdown    = "daemon_shutdown"
	ReasonResourceLimit     = "resource_limit"
	ReasonSpawnError        = "spawn_error"
)

type Limits struct {
	ScrollbackBytes int
	IdleTimeout     time.Duration
	MaxLifetime     time.Duration
	OutputChunkSize int
	OutputFlush     time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		ScrollbackBytes: 256 * 1024,
		IdleTimeout:     5 * time.Minute,
		MaxLifetime:     4 * time.Hour,
		OutputChunkSize: 8 * 1024,
		OutputFlush:     16 * time.Millisecond,
	}
}

type OpenRequest struct {
	RequestID  string
	TerminalID string
	SessionID  string
	ChannelID  string
	CWD        string
	Cols       int
	Rows       int
}

type EventSender interface {
	SendTerminalOpened(req OpenRequest, shell string, cwd string, startedAt time.Time) error
	SendTerminalOutput(terminalID, sessionID, channelID string, seq int64, data []byte) error
	SendTerminalSnapshot(terminalID, sessionID, channelID string, seq int64, data []byte) error
	SendTerminalExit(terminalID, sessionID, channelID, reason string, exitCode int, signal string, endedAt time.Time) error
	SendTerminalError(requestID, terminalID, sessionID, channelID, message string) error
}
