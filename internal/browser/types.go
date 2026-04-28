package browser

import "encoding/json"

type ControlOwner string

const (
	OwnerAgent    ControlOwner = "agent"
	OwnerLex      ControlOwner = "lex"
	OwnerPaused   ControlOwner = "paused"
	OwnerApproval ControlOwner = "approval"
)

type OpenRequest struct {
	GrantID    string
	SessionID  string
	ProjectID  string
	TaskID     string
	ChannelID  string
	MachineID  string
	IdentityID string
	Mode       string
	ExpiresAt  string
}

type OpenResult struct {
	BrowserID string
	URL       string
	Title     string
}

type Frame struct {
	Sequence    int64
	ContentType string
	DataBase64  string
	Width       int
	Height      int
	CapturedAt  string
	URL         string
	Title       string
}

type ToolResult struct {
	OK         bool
	ResultJSON json.RawMessage
	Error      string
}
