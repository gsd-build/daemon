package agentterminal

import "time"

const (
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusReady    = "ready"
	StatusExited   = "exited"
	StatusFailed   = "failed"
	StatusKilled   = "killed"

	ReadinessUnknown  = "unknown"
	ReadinessWaiting  = "waiting"
	ReadinessReady    = "ready"
	ReadinessTimedOut = "timed_out"
	ReadinessFailed   = "failed"
)

type Limits struct {
	MaxSessionJobs         int
	MaxDaemonJobs          int
	ScrollbackBytes        int
	OutputChunkBytes       int
	ToolOutputBytes        int
	DefaultReadyTimeout    time.Duration
	MaxWaitTimeout         time.Duration
	MaxLifetime            time.Duration
	IdleTimeout            time.Duration
	TerminationGracePeriod time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxSessionJobs:         4,
		MaxDaemonJobs:          12,
		ScrollbackBytes:        512 * 1024,
		OutputChunkBytes:       8 * 1024,
		ToolOutputBytes:        64 * 1024,
		DefaultReadyTimeout:    30 * time.Second,
		MaxWaitTimeout:         120 * time.Second,
		MaxLifetime:            4 * time.Hour,
		IdleTimeout:            30 * time.Minute,
		TerminationGracePeriod: 2 * time.Second,
	}
}

type Readiness struct {
	State       string `json:"state"`
	Source      string `json:"source,omitempty"`
	MatchedText string `json:"matchedText,omitempty"`
	ReadyAt     string `json:"readyAt,omitempty"`
	TimeoutMs   int    `json:"timeoutMs,omitempty"`
}

type Port struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	URL  string `json:"url"`
}

type StartRequest struct {
	Command         string            `json:"command"`
	CWD             string            `json:"cwd,omitempty"`
	Title           string            `json:"title,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	ReadyPattern    string            `json:"readyPattern,omitempty"`
	ReadyURL        string            `json:"readyUrl,omitempty"`
	ReadyPort       int               `json:"readyPort,omitempty"`
	ReadyTimeoutMs  int               `json:"readyTimeoutMs,omitempty"`
	OutputTailBytes int               `json:"outputTailBytes,omitempty"`
	SessionID       string
	ChannelID       string
	TaskID          string
	ToolCallID      string
	ProjectID       string
	MachineID       string
	ProjectCWD      string
}

type StartResult struct {
	JobID       string    `json:"jobId"`
	TerminalID  string    `json:"terminalId"`
	Status      string    `json:"status"`
	Title       string    `json:"title"`
	CWD         string    `json:"cwd"`
	StartedAt   string    `json:"startedAt"`
	Readiness   Readiness `json:"readiness"`
	Ports       []Port    `json:"ports"`
	URLs        []string  `json:"urls"`
	OutputTail  string    `json:"outputTail"`
	NextActions []string  `json:"nextActions"`
}

type OutputRequest struct {
	JobID     string `json:"jobId"`
	SinceSeq  int64  `json:"sinceSeq,omitempty"`
	TailBytes int    `json:"tailBytes,omitempty"`
	TailLines int    `json:"tailLines,omitempty"`
}

type OutputResult struct {
	JobID      string    `json:"jobId"`
	TerminalID string    `json:"terminalId"`
	Status     string    `json:"status"`
	Seq        int64     `json:"seq"`
	Output     string    `json:"output"`
	Truncated  bool      `json:"truncated"`
	Readiness  Readiness `json:"readiness"`
	Ports      []Port    `json:"ports"`
	URLs       []string  `json:"urls"`
	ExitCode   *int      `json:"exitCode,omitempty"`
	Signal     string    `json:"signal,omitempty"`
	EndedAt    string    `json:"endedAt,omitempty"`
}

type WaitRequest struct {
	JobID     string `json:"jobId"`
	Condition string `json:"condition"`
	Pattern   string `json:"pattern,omitempty"`
	TimeoutMs int    `json:"timeoutMs"`
	SinceSeq  int64  `json:"sinceSeq,omitempty"`
}

type WaitResult struct {
	JobID      string    `json:"jobId"`
	Matched    bool      `json:"matched"`
	Condition  string    `json:"condition"`
	Status     string    `json:"status"`
	Seq        int64     `json:"seq"`
	OutputTail string    `json:"outputTail"`
	Readiness  Readiness `json:"readiness"`
	TimedOut   bool      `json:"timedOut"`
}

type SendRequest struct {
	JobID         string `json:"jobId"`
	Input         string `json:"input"`
	AppendNewline bool   `json:"appendNewline,omitempty"`
}

type SendResult struct {
	JobID  string `json:"jobId"`
	Sent   bool   `json:"sent"`
	Seq    int64  `json:"seq"`
	Status string `json:"status"`
}

type KillRequest struct {
	JobID  string `json:"jobId"`
	Signal string `json:"signal,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type KillResult struct {
	JobID    string `json:"jobId"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Signal   string `json:"signal,omitempty"`
	EndedAt  string `json:"endedAt"`
}

type ListRequest struct {
	Status string `json:"status,omitempty"`
}

type JobSummary struct {
	JobID          string    `json:"jobId"`
	TerminalID     string    `json:"terminalId"`
	Title          string    `json:"title"`
	CommandPreview string    `json:"commandPreview"`
	CWD            string    `json:"cwd"`
	Status         string    `json:"status"`
	Readiness      Readiness `json:"readiness"`
	Ports          []Port    `json:"ports"`
	URLs           []string  `json:"urls"`
	StartedAt      string    `json:"startedAt"`
	EndedAt        string    `json:"endedAt,omitempty"`
}

type ListResult struct {
	Jobs []JobSummary `json:"jobs"`
}

type TaskScope struct {
	SessionID  string
	ChannelID  string
	TaskID     string
	ProjectID  string
	MachineID  string
	ProjectCWD string
}

type TaskControl struct {
	SocketPath string
	Token      string
	Env        []string
}

type ShellExecRequest struct {
	Command    string `json:"command"`
	CWD        string `json:"cwd,omitempty"`
	TimeoutMs  int    `json:"timeoutMs,omitempty"`
	Mode       string `json:"mode,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

type ShellExecResult struct {
	Mode       string       `json:"mode"`
	Background bool         `json:"background"`
	Started    *StartResult `json:"started,omitempty"`
	Stdout     string       `json:"stdout,omitempty"`
	Stderr     string       `json:"stderr,omitempty"`
	ExitCode   int          `json:"exitCode"`
	TimedOut   bool         `json:"timedOut"`
	Truncated  bool         `json:"truncated,omitempty"`
}

type Job struct {
	JobID          string
	TerminalID     string
	SessionID      string
	ChannelID      string
	TaskID         string
	ToolCallID     string
	ProjectID      string
	MachineID      string
	Command        string
	CommandPreview string
	Title          string
	CWD            string
	Status         string
	Readiness      Readiness
	Ports          []Port
	URLs           []string
	Seq            int64
	StartedAt      time.Time
	UpdatedAt      time.Time
	EndedAt        time.Time
	ExitCode       *int
	Signal         string
	Reason         string
}
