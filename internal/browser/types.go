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
	InitialURL string
	BridgeMode string
	PreviewID  string
	ExpiresAt  string
}

type OpenResult struct {
	BrowserID string
	URL       string
	Title     string
}

type Frame struct {
	Sequence         int64
	ContentType      string
	DataBase64       string
	Width            int
	Height           int
	ViewportWidth    int
	ViewportHeight   int
	DevicePixelRatio float64
	CapturedAt       string
	URL              string
	Title            string
}

type Refs struct {
	Version    int
	Refs       []Ref
	CapturedAt string
}

type Ref struct {
	Ref  string  `json:"ref"`
	Key  string  `json:"key"`
	Role string  `json:"role"`
	Name string  `json:"name,omitempty"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
}

type ToolResult struct {
	OK         bool
	ResultJSON json.RawMessage
	Error      string
}
