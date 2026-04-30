package lab

import "time"

const ExportSchemaVersion = 1

type SessionConfig struct {
	CWD            string `json:"cwd"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	PermissionMode string `json:"permissionMode"`
	FakeMode       bool   `json:"fakeMode"`
}

type SessionStoreOptions struct {
	RootDir string
	Config  SessionConfig
}

type SessionEvent struct {
	Sequence  int64         `json:"sequence"`
	ID        string        `json:"id"`
	Kind      string        `json:"kind"`
	Timestamp string        `json:"timestamp"`
	Payload   any           `json:"payload,omitempty"`
	Redacted  []RedactEvent `json:"redacted,omitempty"`
}

type ExportOptions struct {
	IncludeSensitive bool `json:"includeSensitive"`
}

type ExportBundle struct {
	SchemaVersion int            `json:"schemaVersion"`
	ExportedAt    string         `json:"exportedAt"`
	Config        SessionConfig  `json:"config"`
	Events        []SessionEvent `json:"events"`
}

type RedactEvent struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Hash   string `json:"hash"`
}

type AnalysisReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	EventCount    int    `json:"eventCount"`
	ErrorCount    int    `json:"errorCount"`
	ToolCallCount int    `json:"toolCallCount"`
	QuestionCount int    `json:"questionCount"`
	TerminalCount int    `json:"terminalCount"`
	PlanCallCount int    `json:"planCallCount"`
}

func nowRFC3339Nano() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
