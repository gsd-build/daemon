package agentterminal

import "time"

type EventSender interface {
	SendAgentTerminalStarted(job Job) error
	SendAgentTerminalUpdated(job Job) error
	SendTerminalOutput(terminalID, sessionID, channelID string, seq int64, data []byte) error
	SendTerminalSnapshot(terminalID, sessionID, channelID string, seq int64, data []byte) error
	SendTerminalExit(terminalID, sessionID, channelID, reason string, exitCode int, signal string, endedAt time.Time) error
	SendTerminalError(requestID, terminalID, sessionID, channelID, message string) error
	SendLocalServerDetected(sessionID, channelID, taskID, toolCallID, host string, port int, url, command, source string) error
}
