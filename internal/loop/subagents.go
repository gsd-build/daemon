package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/gsd-build/daemon/internal/api"
	"github.com/gsd-build/daemon/internal/pi"
	"github.com/gsd-build/daemon/internal/sockapi"
	protocol "github.com/gsd-build/protocol-go"
)

func (d *Daemon) CreateSubagentChild(_ *http.Request, req sockapi.CreateSubagentChildRequest) (sockapi.CreateSubagentChildResponse, error) {
	if req.ParentSessionID == "" || req.ParentToolCallID == "" || req.AgentName == "" || req.Task == "" {
		return sockapi.CreateSubagentChildResponse{}, fmt.Errorf("%w: missing required child fields", sockapi.ErrBadSubagentRequest)
	}
	resp, err := api.NewClient(d.cfg.ServerURL).CreateSubagentChild(api.CreateSubagentChildRequest{
		MachineID:        d.cfg.MachineID,
		AuthToken:        d.cfg.AuthToken,
		ParentSessionID:  req.ParentSessionID,
		ParentToolCallID: req.ParentToolCallID,
		AgentName:        req.AgentName,
		Task:             req.Task,
	})
	if err != nil {
		return sockapi.CreateSubagentChildResponse{}, err
	}

	d.subagentMu.Lock()
	if d.subagentStreams == nil {
		d.subagentStreams = make(map[string]*pi.ChildTranslator)
	}
	if d.subagentSeq == nil {
		d.subagentSeq = make(map[string]int64)
	}
	if d.subagentProcesses == nil {
		d.subagentProcesses = make(map[string]int)
	}
	d.subagentStreams[resp.ChildSessionID] = pi.NewChildTranslator(resp.ChildSessionID, "", resp.Agent.Model)
	d.subagentSeq[resp.ChildSessionID] = 0
	d.subagentMu.Unlock()

	return sockapi.CreateSubagentChildResponse{
		ChildSessionID:  resp.ChildSessionID,
		ParentSessionID: resp.ParentSessionID,
		ProjectID:       resp.ProjectID,
		AgentName:       resp.Agent.Name,
		Model:           resp.Agent.Model,
	}, nil
}

func (d *Daemon) RegisterSubagentProcess(_ *http.Request, req sockapi.RegisterSubagentProcessRequest) error {
	if req.ChildSessionID == "" || req.PID <= 0 {
		return fmt.Errorf("%w: missing child process fields", sockapi.ErrBadSubagentRequest)
	}
	d.subagentMu.Lock()
	if d.subagentProcesses == nil {
		d.subagentProcesses = make(map[string]int)
	}
	d.subagentProcesses[req.ChildSessionID] = req.PID
	d.subagentMu.Unlock()
	return nil
}

func (d *Daemon) ForwardSubagentEvent(_ *http.Request, req sockapi.ForwardSubagentEventRequest) error {
	if req.ChildSessionID == "" || req.Event == "" {
		return fmt.Errorf("%w: missing child session event fields", sockapi.ErrBadSubagentRequest)
	}

	d.subagentMu.Lock()
	if d.subagentStreams == nil {
		d.subagentStreams = make(map[string]*pi.ChildTranslator)
	}
	translator := d.subagentStreams[req.ChildSessionID]
	if translator == nil {
		translator = pi.NewChildTranslator(req.ChildSessionID, "", "")
		d.subagentStreams[req.ChildSessionID] = translator
	}
	events := translator.Translate(json.RawMessage(req.Event))
	d.subagentMu.Unlock()

	for _, event := range events {
		sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := d.client.Send(sendCtx, &protocol.Stream{
			Type:           protocol.MsgTypeStream,
			SessionID:      req.ChildSessionID,
			ChannelID:      "subagent:" + req.ChildSessionID,
			SequenceNumber: d.nextSubagentSequence(req.ChildSessionID),
			Event:          event,
		})
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) FinalizeSubagentChild(_ *http.Request, req sockapi.FinalizeSubagentChildRequest) (sockapi.FinalizeSubagentChildResponse, error) {
	if req.ChildSessionID == "" {
		return sockapi.FinalizeSubagentChildResponse{}, fmt.Errorf("%w: childSessionId is required", sockapi.ErrBadSubagentRequest)
	}
	status := req.Status
	if status == "" {
		status = "done"
	}
	cost := req.TotalCostUSD
	if cost == "" {
		cost = "0"
	}
	resp, err := api.NewClient(d.cfg.ServerURL).FinalizeSubagentChild(api.FinalizeSubagentChildRequest{
		MachineID:         d.cfg.MachineID,
		AuthToken:         d.cfg.AuthToken,
		ChildSessionID:    req.ChildSessionID,
		Status:            status,
		TotalInputTokens:  req.TotalInputTokens,
		TotalOutputTokens: req.TotalOutputTokens,
		TotalCostUSD:      cost,
		TurnCount:         req.TurnCount,
		FinalText:         req.FinalText,
		ErrorMessage:      req.ErrorMessage,
	})
	if err != nil {
		return sockapi.FinalizeSubagentChildResponse{}, err
	}
	d.sendSubagentStatus(req.ChildSessionID, status, req.TotalInputTokens, req.TotalOutputTokens, cost, req.ErrorMessage)

	d.subagentMu.Lock()
	delete(d.subagentStreams, req.ChildSessionID)
	delete(d.subagentSeq, req.ChildSessionID)
	delete(d.subagentProcesses, req.ChildSessionID)
	d.subagentMu.Unlock()

	return sockapi.FinalizeSubagentChildResponse{
		OK:              resp.OK,
		Status:          resp.Status,
		ChildSessionID:  resp.ChildSessionID,
		ParentSessionID: resp.ParentSessionID,
		ProjectID:       resp.ProjectID,
		FinalText:       resp.FinalText,
		ErrorMessage:    resp.ErrorMessage,
	}, nil
}

func (d *Daemon) nextSubagentSequence(sessionID string) int64 {
	d.subagentMu.Lock()
	defer d.subagentMu.Unlock()
	if d.subagentSeq == nil {
		d.subagentSeq = make(map[string]int64)
	}
	next := d.subagentSeq[sessionID] + 1
	d.subagentSeq[sessionID] = next
	return next
}

func (d *Daemon) sendSubagentStatus(sessionID string, status string, inputTokens int64, outputTokens int64, costUSD string, errorMessage string) {
	if d.client == nil {
		return
	}
	payload := map[string]any{
		"type":         "subagent_status",
		"status":       status,
		"inputTokens":  inputTokens,
		"outputTokens": outputTokens,
		"costUsd":      costUSD,
	}
	if errorMessage != "" {
		payload["error"] = errorMessage
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.client.Send(sendCtx, &protocol.Stream{
		Type:           protocol.MsgTypeStream,
		SessionID:      sessionID,
		ChannelID:      "subagent:" + sessionID,
		SequenceNumber: d.nextSubagentSequence(sessionID),
		Event:          raw,
	})
}

func (d *Daemon) cancelSubagentChild(sessionID string) bool {
	d.subagentMu.Lock()
	pid := 0
	if d.subagentProcesses != nil {
		pid = d.subagentProcesses[sessionID]
		delete(d.subagentProcesses, sessionID)
	}
	d.subagentMu.Unlock()
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
		return true
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		<-timer.C
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}()
	return true
}
