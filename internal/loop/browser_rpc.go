package loop

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/gsd-build/daemon/internal/browser"
	protocol "github.com/gsd-build/protocol-go"
)

type browserRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      json.RawMessage        `json:"id,omitempty"`
	Method  string                 `json:"method"`
	Params  browser.ToolRPCRequest `json:"params"`
}

type browserRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *browserRPCError `json:"error,omitempty"`
}

type browserRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (d *Daemon) runBrowserRPC(ctx context.Context) error {
	if d.browserManager == nil || d.browserRPCSocket == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(d.browserRPCSocket), 0o700); err != nil {
		return err
	}
	_ = os.Remove(d.browserRPCSocket)
	listener, err := net.Listen("unix", d.browserRPCSocket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(d.browserRPCSocket)
	_ = os.Chmod(d.browserRPCSocket, 0o600)

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go d.handleBrowserRPCConn(ctx, conn)
	}
}

func (d *Daemon) handleBrowserRPCConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	deadline := time.Now().Add(35 * time.Second)
	_ = conn.SetDeadline(deadline)
	payload, err := readBrowserRPCFrame(conn)
	if err != nil {
		slog.Debug("browser rpc read failed", "err", err)
		return
	}
	var req browserRPCRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		_ = writeBrowserRPCFrame(conn, browserRPCResponse{
			JSONRPC: "2.0",
			Error:   &browserRPCError{Code: -32700, Message: "invalid browser rpc request"},
		})
		return
	}
	resp := d.handleBrowserRPC(ctx, req)
	_ = writeBrowserRPCFrame(conn, resp)
}

func (d *Daemon) handleBrowserRPC(ctx context.Context, req browserRPCRequest) browserRPCResponse {
	resp := browserRPCResponse{JSONRPC: "2.0", ID: req.ID}
	if req.Method != "browser_tool" {
		resp.Error = &browserRPCError{Code: -32601, Message: "unknown browser rpc method"}
		return resp
	}
	grant, err := d.browserManager.Ensure(ctx, browser.EnsureRequest{
		GrantID:   req.Params.GrantID,
		SessionID: req.Params.SessionID,
		ProjectID: req.Params.ProjectID,
		TaskID:    req.Params.TaskID,
		ChannelID: req.Params.ChannelID,
		MachineID: req.Params.MachineID,
		ExpiresAt: req.Params.ExpiresAt,
	})
	if err != nil {
		resp.Error = &browserRPCError{Code: -32000, Message: err.Error()}
		return resp
	}
	toolUseID := req.Params.ToolUseID
	if toolUseID == "" {
		toolUseID = fmt.Sprintf("browser_tool_%d", time.Now().UnixNano())
	}
	params := req.Params.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	result, err := d.browserManager.ToolResult(ctx, &protocol.BrowserToolCall{
		Type:       protocol.MsgTypeBrowserToolCall,
		BrowserID:  grant.BrowserID,
		GrantID:    grant.GrantID,
		TaskID:     req.Params.TaskID,
		ToolUseID:  toolUseID,
		Method:     req.Params.Method,
		ParamsJSON: params,
	})
	if err != nil {
		resp.Error = &browserRPCError{Code: -32001, Message: err.Error()}
		return resp
	}
	if len(result.ResultJSON) == 0 {
		resp.Result = map[string]any{"ok": result.OK}
		return resp
	}
	var decoded any
	if err := json.Unmarshal(result.ResultJSON, &decoded); err != nil {
		resp.Result = string(result.ResultJSON)
		return resp
	}
	resp.Result = decoded
	return resp
}

func readBrowserRPCFrame(r io.Reader) ([]byte, error) {
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

func writeBrowserRPCFrame(w io.Writer, resp browserRPCResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if err := writeBrowserRPCFull(w, header[:]); err != nil {
		return err
	}
	return writeBrowserRPCFull(w, data)
}

func writeBrowserRPCFull(w io.Writer, data []byte) error {
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
