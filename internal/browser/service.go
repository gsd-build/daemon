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
	if req.Mode == "identity" && req.IdentityID != "" {
		args = append(args,
			"--identity-scope", "project",
			"--identity-key", req.IdentityID,
			"--identity-project", req.ProjectID,
		)
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
		Sequence     int64  `json:"sequence"`
		ContentType  string `json:"contentType"`
		DataBase64   string `json:"dataBase64"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		CapturedAtMs int64  `json:"capturedAtMs"`
		URL          string `json:"url"`
		Title        string `json:"title"`
	}
	if err := s.rpc(ctx, browserID, "cloud_frame", map[string]any{"quality": 70}, &out); err != nil {
		return Frame{}, err
	}
	return Frame{
		Sequence:    out.Sequence,
		ContentType: out.ContentType,
		DataBase64:  out.DataBase64,
		Width:       out.Width,
		Height:      out.Height,
		CapturedAt:  time.UnixMilli(out.CapturedAtMs).UTC().Format(time.RFC3339Nano),
		URL:         out.URL,
		Title:       out.Title,
	}, nil
}

func (s LocalService) UserInput(ctx context.Context, browserID string, input *protocol.BrowserUserInput) error {
	params := map[string]any{
		"kind": input.Kind,
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
	if input.DeltaX != nil {
		params["deltaX"] = *input.DeltaX
	}
	if input.DeltaY != nil {
		params["deltaY"] = *input.DeltaY
	}
	return s.rpc(ctx, browserID, "cloud_user_input", params, nil)
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
	if err := writeFrame(conn, data); err != nil {
		return err
	}
	buf, err := readFrame(conn)
	if err != nil {
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
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
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
