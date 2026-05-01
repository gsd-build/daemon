package browser

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

type Service interface {
	Open(context.Context, OpenRequest) (OpenResult, error)
	Close(context.Context, string) error
	Frame(context.Context, string) (Frame, error)
	Refs(context.Context, string) (Refs, error)
	Tool(context.Context, string, string, []byte) (ToolResult, error)
	UserInput(context.Context, string, *protocol.BrowserUserInput) error
}

type LocalService struct {
	BinaryPath string
	StateDir   string
}

func (s LocalService) socketPath(grantID string) string {
	return filepath.Join(s.StateDir, "sessions", grantID, "daemon.sock")
}

func (s LocalService) binaryPath() string {
	if s.BinaryPath != "" {
		return s.BinaryPath
	}
	return "gsd-browser"
}

func (s LocalService) Start(ctx context.Context, req OpenRequest) error {
	args := []string{"--session", req.GrantID}
	if req.Mode == "identity" && req.IdentityKey != "" {
		args = append(args, "--identity-scope", req.IdentityScope, "--identity-key", req.IdentityKey)
		if req.IdentityScope == "project" {
			args = append(args, "--identity-project", req.IdentityProjectID)
		}
	}
	args = append(args, "daemon", "start")
	cmd := exec.CommandContext(ctx, s.binaryPath(), args...)
	return cmd.Run()
}

func (s LocalService) Open(ctx context.Context, req OpenRequest) (OpenResult, error) {
	if err := s.Start(ctx, req); err != nil {
		return OpenResult{}, err
	}
	var status struct {
		ActiveURL   string `json:"activeUrl"`
		ActiveTitle string `json:"activeTitle"`
	}
	if err := s.rpc(ctx, req.GrantID, "cloud_session_status", map[string]any{}, &status); err != nil {
		return OpenResult{}, err
	}
	return OpenResult{
		BrowserID: req.GrantID,
		URL:       status.ActiveURL,
		Title:     status.ActiveTitle,
	}, nil
}

func (s LocalService) Tool(ctx context.Context, browserID string, method string, params []byte) (ToolResult, error) {
	var rawParams json.RawMessage = params
	if len(rawParams) == 0 {
		rawParams = json.RawMessage(`{}`)
	}
	var out json.RawMessage
	if err := s.rpc(ctx, browserID, "cloud_tool", map[string]any{
		"method": method,
		"params": rawParams,
	}, &out); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{OK: true, ResultJSON: out}, nil
}

func (s LocalService) Frame(ctx context.Context, browserID string) (Frame, error) {
	var out struct {
		Sequence           int64   `json:"sequence"`
		ContentType        string  `json:"contentType"`
		DataBase64         string  `json:"dataBase64"`
		Width              int     `json:"width"`
		Height             int     `json:"height"`
		ViewportWidth      int     `json:"viewportWidth"`
		ViewportHeight     int     `json:"viewportHeight"`
		ViewportCSSWidth   int     `json:"viewportCssWidth"`
		ViewportCSSHeight  int     `json:"viewportCssHeight"`
		CapturePixelWidth  int     `json:"capturePixelWidth"`
		CapturePixelHeight int     `json:"capturePixelHeight"`
		DevicePixelRatio   float64 `json:"devicePixelRatio"`
		CaptureScaleX      float64 `json:"captureScaleX"`
		CaptureScaleY      float64 `json:"captureScaleY"`
		EncodedBytes       int     `json:"encodedBytes"`
		Quality            int     `json:"quality"`
		CapturePixelRatio  float64 `json:"capturePixelRatio"`
		CapturedAtMs       int64   `json:"capturedAtMs"`
		URL                string  `json:"url"`
		Title              string  `json:"title"`
	}
	if err := s.rpc(ctx, browserID, "cloud_frame", map[string]any{"quality": 70}, &out); err != nil {
		return Frame{}, err
	}
	return Frame{
		Sequence:           out.Sequence,
		ContentType:        out.ContentType,
		DataBase64:         out.DataBase64,
		Width:              out.Width,
		Height:             out.Height,
		ViewportWidth:      out.ViewportWidth,
		ViewportHeight:     out.ViewportHeight,
		ViewportCSSWidth:   out.ViewportCSSWidth,
		ViewportCSSHeight:  out.ViewportCSSHeight,
		CapturePixelWidth:  out.CapturePixelWidth,
		CapturePixelHeight: out.CapturePixelHeight,
		DevicePixelRatio:   out.DevicePixelRatio,
		CaptureScaleX:      out.CaptureScaleX,
		CaptureScaleY:      out.CaptureScaleY,
		EncodedBytes:       out.EncodedBytes,
		Quality:            out.Quality,
		CapturePixelRatio:  out.CapturePixelRatio,
		CapturedAt:         time.UnixMilli(out.CapturedAtMs).UTC().Format(time.RFC3339Nano),
		URL:                out.URL,
		Title:              out.Title,
	}, nil
}

func (s LocalService) Refs(ctx context.Context, browserID string) (Refs, error) {
	var out struct {
		Version      int    `json:"version"`
		Refs         []Ref  `json:"refs"`
		CapturedAtMs int64  `json:"capturedAtMs"`
		CapturedAt   string `json:"capturedAt"`
	}
	if err := s.rpc(ctx, browserID, "cloud_refs", map[string]any{}, &out); err != nil {
		return Refs{}, err
	}
	capturedAt := out.CapturedAt
	if capturedAt == "" && out.CapturedAtMs != 0 {
		capturedAt = time.UnixMilli(out.CapturedAtMs).UTC().Format(time.RFC3339Nano)
	}
	return Refs{Version: out.Version, Refs: out.Refs, CapturedAt: capturedAt}, nil
}

func (s LocalService) UserInput(ctx context.Context, browserID string, input *protocol.BrowserUserInput) error {
	if method, params, ok := browserUserInputTool(input); ok {
		_, err := s.Tool(ctx, browserID, method, params)
		return err
	}
	return s.rpc(ctx, browserID, "cloud_user_input", browserUserInputParams(input), nil)
}

func browserUserInputTool(input *protocol.BrowserUserInput) (string, []byte, bool) {
	switch input.Kind {
	case protocol.BrowserInputKindNavigate:
		url := input.URL
		if url == "" {
			url = input.Text
		}
		params, err := json.Marshal(map[string]any{"url": url})
		return "navigate", params, err == nil
	case protocol.BrowserInputKindBack:
		return "back", []byte(`{}`), true
	case protocol.BrowserInputKindForward:
		return "forward", []byte(`{}`), true
	case protocol.BrowserInputKindReload:
		return "reload", []byte(`{}`), true
	case protocol.BrowserInputKindRefAction:
		params, err := json.Marshal(map[string]any{"ref": input.Text})
		return "click_ref", params, err == nil
	case protocol.BrowserInputKindSetViewport:
		params, err := json.Marshal(map[string]any{
			"width":  input.ViewportWidth,
			"height": input.ViewportHeight,
		})
		return "set_viewport", params, err == nil
	case protocol.BrowserInputKindEmulateDevice:
		params, err := json.Marshal(map[string]any{"device": input.Text})
		return "emulate_device", params, err == nil
	default:
		return "", nil, false
	}
}

func browserUserInputParams(input *protocol.BrowserUserInput) map[string]any {
	params := map[string]any{
		"kind": input.Kind,
	}
	if input.InputID != "" {
		params["inputId"] = input.InputID
	}
	if input.X != nil {
		params["x"] = *input.X
	}
	if input.Y != nil {
		params["y"] = *input.Y
	}
	if input.Text != "" {
		params["text"] = input.Text
	}
	if input.Key != "" {
		params["key"] = input.Key
	}
	if input.Phase != "" {
		params["phase"] = input.Phase
	}
	if input.Button != "" {
		params["button"] = input.Button
	}
	if input.Buttons != 0 {
		params["buttons"] = input.Buttons
	}
	if input.ClickCount != 0 {
		params["clickCount"] = input.ClickCount
	}
	if input.PointerType != "" {
		params["pointerType"] = input.PointerType
	}
	if len(input.Modifiers) > 0 {
		params["modifiers"] = input.Modifiers
	}
	if input.Code != "" {
		params["code"] = input.Code
	}
	if input.Location != 0 {
		params["location"] = input.Location
	}
	if input.Repeat {
		params["repeat"] = input.Repeat
	}
	if input.CommitMode != "" {
		params["commitMode"] = input.CommitMode
	}
	if len(input.MimeTypes) > 0 {
		params["mimeTypes"] = input.MimeTypes
	}
	if input.DeltaX != nil {
		params["deltaX"] = *input.DeltaX
	}
	if input.DeltaY != nil {
		params["deltaY"] = *input.DeltaY
	}
	if input.URL != "" {
		params["url"] = input.URL
	}
	if input.Action != "" {
		params["action"] = input.Action
	}
	if input.CapturedAt != "" {
		params["capturedAt"] = input.CapturedAt
	}
	if input.FrameSeq != 0 {
		params["frameSeq"] = input.FrameSeq
	}
	if input.ControlVersion != 0 {
		params["controlVersion"] = input.ControlVersion
	}
	if input.CoordinateSpace != "" {
		params["coordinateSpace"] = input.CoordinateSpace
	}
	if input.ViewportWidth != 0 {
		params["viewportWidth"] = input.ViewportWidth
	}
	if input.ViewportHeight != 0 {
		params["viewportHeight"] = input.ViewportHeight
	}
	if input.FrameWidth != 0 {
		params["frameWidth"] = input.FrameWidth
	}
	if input.FrameHeight != 0 {
		params["frameHeight"] = input.FrameHeight
	}
	if input.DevicePixelRatio != 0 {
		params["devicePixelRatio"] = input.DevicePixelRatio
	}
	if input.CoordinateSpace != "" || input.RenderedWidth != 0 || input.RenderedHeight != 0 {
		params["renderedLeft"] = input.RenderedLeft
		params["renderedTop"] = input.RenderedTop
	}
	if input.RenderedWidth != 0 {
		params["renderedWidth"] = input.RenderedWidth
	}
	if input.RenderedHeight != 0 {
		params["renderedHeight"] = input.RenderedHeight
	}
	return params
}

func (s LocalService) Close(ctx context.Context, browserID string) error {
	cmd := exec.CommandContext(ctx, s.binaryPath(), "--session", browserID, "daemon", "stop")
	return cmd.Run()
}

func (s LocalService) rpc(ctx context.Context, grantID string, method string, params any, out any) error {
	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", s.socketPath(grantID))
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)
	if err := writeFrame(conn, data); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	buf, err := readFrame(conn)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("%s", resp.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

func writeFrame(w io.Writer, data []byte) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if err := writeFull(w, header[:]); err != nil {
		return err
	}
	return writeFull(w, data)
}

func writeFull(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > 16*1024*1024 {
		return nil, fmt.Errorf("browser rpc frame too large: %d", size)
	}
	buf := make([]byte, size)
	_, err := io.ReadFull(r, buf)
	return buf, err
}
