package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ClaimCronRunRequest is the body of POST /api/daemon/cron-runs/claim.
type ClaimCronRunRequest struct {
	MachineID      string `json:"machineId"`
	CronJobID      string `json:"cronJobId"`
	ScheduledFor   string `json:"scheduledFor"`
	IdempotencyKey string `json:"idempotencyKey"`
	AuthToken      string `json:"-"`
}

// ClaimCronTask is the dispatchable task payload returned by the cloud.
type ClaimCronTask struct {
	TaskID              string `json:"taskId"`
	SessionID           string `json:"sessionId"`
	ChannelID           string `json:"channelId"`
	Prompt              string `json:"prompt"`
	Model               string `json:"model"`
	Effort              string `json:"effort"`
	PermissionMode      string `json:"permissionMode"`
	CWD                 string `json:"cwd"`
	PersonaSystemPrompt string `json:"personaSystemPrompt"`
	ClaudeSessionID     string `json:"claudeSessionId,omitempty"`
}

// ClaimCronRunResponse is the successful claim response from the cloud.
type ClaimCronRunResponse struct {
	CronRunID string        `json:"cronRunId"`
	Task      ClaimCronTask `json:"task"`
}

// ClaimCronRun calls POST /api/daemon/cron-runs/claim.
func (c *Client) ClaimCronRun(req ClaimCronRunRequest) (*ClaimCronRunResponse, error) {
	body, err := json.Marshal(map[string]string{
		"machineId":      req.MachineID,
		"cronJobId":      req.CronJobID,
		"scheduledFor":   req.ScheduledFor,
		"idempotencyKey": req.IdempotencyKey,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequest(
		http.MethodPost,
		c.baseURL+"/api/daemon/cron-runs/claim",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+req.AuthToken)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("claim failed: %s: %s", resp.Status, string(data))
	}

	var out ClaimCronRunResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &out, nil
}
