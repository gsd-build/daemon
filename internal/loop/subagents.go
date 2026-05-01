package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

var subagentStatusSendTimeout = 5 * time.Second
var subagentCancelFinalizeDelay = 3 * time.Second

func (d *Daemon) CreateSubagentChild(r *http.Request, req sockapi.CreateSubagentChildRequest) (sockapi.CreateSubagentChildResponse, error) {
	if req.ParentSessionID == "" || req.ParentToolCallID == "" || req.AgentName == "" || req.Task == "" {
		return sockapi.CreateSubagentChildResponse{}, fmt.Errorf("%w: missing required child fields", sockapi.ErrBadSubagentRequest)
	}
	runIndex := req.RunIndex
	if runIndex <= 0 {
		runIndex = 1
	}
	mode := req.Mode
	if mode == "" {
		mode = "single"
	}
	resp, err := api.NewClient(d.cfg.ServerURL).CreateSubagentChild(r.Context(), api.CreateSubagentChildRequest{
		MachineID:        d.cfg.MachineID,
		AuthToken:        d.cfg.AuthToken,
		ParentSessionID:  req.ParentSessionID,
		ParentToolCallID: req.ParentToolCallID,
		RunIndex:         runIndex,
		Mode:             mode,
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
	if d.subagentRunIDs == nil {
		d.subagentRunIDs = make(map[string]string)
	}
	if d.subagentParentSessions == nil {
		d.subagentParentSessions = make(map[string]string)
	}
	d.subagentStreams[resp.ChildSessionID] = pi.NewChildTranslator(resp.ChildSessionID, "", resp.Agent.Model)
	d.subagentSeq[resp.ChildSessionID] = 0
	d.subagentRunIDs[resp.ChildSessionID] = resp.RunID
	d.subagentParentSessions[resp.ChildSessionID] = req.ParentSessionID
	d.subagentMu.Unlock()

	if err := d.sendSubagentStarted(r.Context(), resp.RunID, req.ParentSessionID, req.ParentToolCallID, resp.ChildSessionID, resp.ProjectID, runIndex, mode, req.Task, resp.Agent.Name, resp.Agent.Model); err != nil {
		slog.Warn("send subagent started status failed", "parentSessionId", req.ParentSessionID, "childSessionId", resp.ChildSessionID, "err", err)
	}
	respParentToolCallID := resp.ParentToolCallID
	if respParentToolCallID == "" {
		respParentToolCallID = req.ParentToolCallID
	}
	respRunIndex := resp.RunIndex
	if respRunIndex <= 0 {
		respRunIndex = runIndex
	}
	respMode := resp.Mode
	if respMode == "" {
		respMode = mode
	}

	return sockapi.CreateSubagentChildResponse{
		ChildSessionID:   resp.ChildSessionID,
		ParentSessionID:  resp.ParentSessionID,
		ProjectID:        resp.ProjectID,
		RunID:            resp.RunID,
		ParentToolCallID: respParentToolCallID,
		RunIndex:         respRunIndex,
		Mode:             respMode,
		AgentName:        resp.Agent.Name,
		Model:            resp.Agent.Model,
		Agent: sockapi.SubagentSnapshot{
			ID:           resp.Agent.ID,
			Name:         resp.Agent.Name,
			Description:  resp.Agent.Description,
			SystemPrompt: resp.Agent.SystemPrompt,
			Model:        resp.Agent.Model,
			Tools:        resp.Agent.Tools,
			VersionHash:  resp.Agent.VersionHash,
		},
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
	if req.RunID != "" {
		if d.subagentRunIDs == nil {
			d.subagentRunIDs = make(map[string]string)
		}
		d.subagentRunIDs[req.ChildSessionID] = req.RunID
	}
	d.subagentMu.Unlock()
	return nil
}

func (d *Daemon) HeartbeatSubagentChild(r *http.Request, req sockapi.HeartbeatSubagentChildRequest) error {
	if req.RunID == "" {
		return fmt.Errorf("%w: runId is required", sockapi.ErrBadSubagentRequest)
	}
	status := req.Status
	if status == "" {
		status = "running"
	}
	_, err := api.NewClient(d.cfg.ServerURL).HeartbeatSubagentChild(r.Context(), api.HeartbeatSubagentChildRequest{
		MachineID:      d.cfg.MachineID,
		AuthToken:      d.cfg.AuthToken,
		RunID:          req.RunID,
		ChildSessionID: req.ChildSessionID,
		PID:            req.PID,
		Status:         status,
	})
	return err
}

func (d *Daemon) ForwardSubagentEvent(_ *http.Request, req sockapi.ForwardSubagentEventRequest) error {
	if req.ChildSessionID == "" || len(req.Raw) == 0 {
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
	events := translator.Translate(req.Raw)
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

func (d *Daemon) ParentSessionForSubagentChild(childSessionID string) (string, bool) {
	d.subagentMu.Lock()
	defer d.subagentMu.Unlock()
	if d.subagentParentSessions == nil {
		return "", false
	}
	parent, ok := d.subagentParentSessions[childSessionID]
	return parent, ok
}

func (d *Daemon) sendSubagentStarted(ctx context.Context, runID string, parentSessionID string, parentToolCallID string, childSessionID string, projectID string, runIndex int, mode string, task string, agentName string, model string) error {
	if d.manager == nil {
		return nil
	}
	actor := d.manager.Get(parentSessionID)
	if actor == nil {
		return nil
	}
	payload := map[string]any{
		"type":             "subagent_status",
		"status":           "started",
		"runId":            runID,
		"parentSessionId":  parentSessionID,
		"parentToolCallId": parentToolCallID,
		"childSessionId":   childSessionID,
		"projectId":        projectID,
		"runIndex":         runIndex,
		"mode":             mode,
		"task":             task,
		"agentName":        agentName,
		"model":            model,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return actor.SendStreamEvent(ctx, "subagent:"+childSessionID, raw)
}

func (d *Daemon) FinalizeSubagentChild(r *http.Request, req sockapi.FinalizeSubagentChildRequest) (sockapi.FinalizeSubagentChildResponse, error) {
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
	resp, err := api.NewClient(d.cfg.ServerURL).FinalizeSubagentChild(r.Context(), api.FinalizeSubagentChildRequest{
		MachineID:         d.cfg.MachineID,
		AuthToken:         d.cfg.AuthToken,
		RunID:             req.RunID,
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

	sendErr := d.sendSubagentStatus(req.ChildSessionID, resp.Status, req.TotalInputTokens, req.TotalOutputTokens, cost, resp.ErrorMessage)

	d.subagentMu.Lock()
	delete(d.subagentStreams, req.ChildSessionID)
	delete(d.subagentSeq, req.ChildSessionID)
	delete(d.subagentProcesses, req.ChildSessionID)
	delete(d.subagentRunIDs, req.ChildSessionID)
	delete(d.subagentParentSessions, req.ChildSessionID)
	d.subagentMu.Unlock()
	if sendErr != nil {
		slog.Warn("send subagent terminal status failed", "childSessionId", req.ChildSessionID, "err", sendErr)
	}

	return sockapi.FinalizeSubagentChildResponse{
		OK:              resp.OK,
		Status:          resp.Status,
		RunID:           resp.RunID,
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

func (d *Daemon) sendSubagentStatus(sessionID string, status string, inputTokens int64, outputTokens int64, costUSD string, errorMessage string) error {
	if d.client == nil {
		return nil
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
		return err
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), subagentStatusSendTimeout)
	defer cancel()
	return d.client.Send(sendCtx, &protocol.Stream{
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
	}
	runID := ""
	if d.subagentRunIDs != nil {
		runID = d.subagentRunIDs[sessionID]
	}
	d.subagentMu.Unlock()
	if pid <= 0 {
		if runID != "" {
			d.scheduleCancelledSubagentFallback(sessionID, runID)
			return true
		}
		return false
	}
	if runtime.GOOS == "windows" {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return false
		}
		if err := proc.Kill(); err != nil {
			return false
		}
		d.deleteSubagentProcess(sessionID)
		d.scheduleCancelledSubagentFallback(sessionID, runID)
		return true
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		return false
	}
	d.deleteSubagentProcess(sessionID)
	d.scheduleCancelledSubagentFallback(sessionID, runID)
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		<-timer.C
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}()
	return true
}

func (d *Daemon) cancelSubagentChildrenForParent(parentSessionID string) bool {
	d.subagentMu.Lock()
	children := make([]string, 0)
	for childSessionID, trackedParentSessionID := range d.subagentParentSessions {
		if trackedParentSessionID == parentSessionID {
			children = append(children, childSessionID)
		}
	}
	d.subagentMu.Unlock()

	cancelled := false
	for _, childSessionID := range children {
		if d.cancelSubagentChild(childSessionID) {
			cancelled = true
		}
	}
	return cancelled
}

func (d *Daemon) scheduleCancelledSubagentFallback(sessionID string, runID string) {
	if runID == "" {
		return
	}
	go func() {
		timer := time.NewTimer(subagentCancelFinalizeDelay)
		defer timer.Stop()
		<-timer.C

		d.subagentMu.Lock()
		stillTracked := d.subagentRunIDs != nil && d.subagentRunIDs[sessionID] == runID
		d.subagentMu.Unlock()
		if !stillTracked {
			return
		}
		d.finalizeCancelledSubagent(sessionID, runID)
	}()
}

func (d *Daemon) finalizeCancelledSubagent(sessionID string, runID string) {
	if runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := api.NewClient(d.cfg.ServerURL).FinalizeSubagentChild(ctx, api.FinalizeSubagentChildRequest{
		MachineID:      d.cfg.MachineID,
		AuthToken:      d.cfg.AuthToken,
		RunID:          runID,
		ChildSessionID: sessionID,
		Status:         "cancelled",
		TotalCostUSD:   "0",
		TurnCount:      1,
		ErrorMessage:   "subagent cancelled",
	})
	if err != nil {
		slog.Warn("finalize cancelled subagent failed", "childSessionId", sessionID, "err", err)
	}
	d.subagentMu.Lock()
	delete(d.subagentStreams, sessionID)
	delete(d.subagentSeq, sessionID)
	delete(d.subagentProcesses, sessionID)
	delete(d.subagentRunIDs, sessionID)
	delete(d.subagentParentSessions, sessionID)
	d.subagentMu.Unlock()
}

func (d *Daemon) deleteSubagentProcess(sessionID string) {
	d.subagentMu.Lock()
	defer d.subagentMu.Unlock()
	if d.subagentProcesses != nil {
		delete(d.subagentProcesses, sessionID)
	}
}
