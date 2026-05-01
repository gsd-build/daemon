// Package api contains HTTP clients for the GSD Cloud web app.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is a thin wrapper around net/http for the web app API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a Client pointed at a base URL (e.g. https://app.gsd.build).
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// PairRequest is the body of POST /api/daemon/pair.
type PairRequest struct {
	Code             string `json:"code"`
	Hostname         string `json:"hostname"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	DaemonVersion    string `json:"daemonVersion"`
	InstallationID   string `json:"installationId,omitempty"`
	CurrentMachineID string `json:"currentMachineId,omitempty"`
}

// PairResponse is the successful response from the pairing endpoint.
type PairResponse struct {
	MachineID      string `json:"machineId"`
	AuthToken      string `json:"authToken"`
	TokenExpiresAt string `json:"tokenExpiresAt"`
	RelayURL       string `json:"relayUrl"`
}

// RefreshTokenRequest is the body of POST /api/daemon/refresh-token.
type RefreshTokenRequest struct {
	MachineID string `json:"machineId"`
	Token     string `json:"token"`
}

// RefreshTokenResponse is the successful response from the refresh-token endpoint.
type RefreshTokenResponse struct {
	AuthToken      string `json:"authToken"`
	TokenExpiresAt string `json:"tokenExpiresAt"`
}

type SubagentDefinition struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemPrompt string   `json:"systemPrompt"`
	Model        string   `json:"model"`
	Tools        []string `json:"tools"`
	VersionHash  string   `json:"versionHash"`
}

type ListSubagentsRequest struct {
	MachineID string
	AuthToken string
	SessionID string
}

type ListSubagentsResponse struct {
	ProjectID string               `json:"projectId"`
	SessionID string               `json:"sessionId"`
	Agents    []SubagentDefinition `json:"agents"`
}

type CreateSubagentChildRequest struct {
	MachineID        string `json:"machineId"`
	AuthToken        string `json:"-"`
	ParentSessionID  string `json:"parentSessionId"`
	ParentMessageID  string `json:"parentMessageId,omitempty"`
	ParentToolCallID string `json:"parentToolCallId"`
	RunIndex         int    `json:"runIndex"`
	Mode             string `json:"mode"`
	AgentName        string `json:"agentName"`
	Task             string `json:"task"`
}

type CreateSubagentChildResponse struct {
	RunID            string             `json:"runId"`
	ChildSessionID   string             `json:"childSessionId"`
	ParentSessionID  string             `json:"parentSessionId"`
	ProjectID        string             `json:"projectId"`
	ParentToolCallID string             `json:"parentToolCallId"`
	RunIndex         int                `json:"runIndex"`
	Mode             string             `json:"mode"`
	Status           string             `json:"status"`
	Agent            SubagentDefinition `json:"agent"`
}

type HeartbeatSubagentChildRequest struct {
	MachineID      string `json:"machineId"`
	AuthToken      string `json:"-"`
	RunID          string `json:"runId"`
	ChildSessionID string `json:"childSessionId,omitempty"`
	PID            int    `json:"pid,omitempty"`
	Status         string `json:"status,omitempty"`
}

type HeartbeatSubagentChildResponse struct {
	OK     bool   `json:"ok"`
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

type FinalizeSubagentChildRequest struct {
	MachineID         string `json:"machineId"`
	AuthToken         string `json:"-"`
	RunID             string `json:"runId,omitempty"`
	ChildSessionID    string `json:"childSessionId"`
	Status            string `json:"status"`
	TotalInputTokens  int64  `json:"totalInputTokens"`
	TotalOutputTokens int64  `json:"totalOutputTokens"`
	TotalCostUSD      string `json:"totalCostUsd"`
	TurnCount         int    `json:"turnCount"`
	FinalText         string `json:"finalText,omitempty"`
	ErrorMessage      string `json:"errorMessage,omitempty"`
}

type FinalizeSubagentChildResponse struct {
	OK              bool   `json:"ok"`
	Status          string `json:"status"`
	RunID           string `json:"runId"`
	ChildSessionID  string `json:"childSessionId"`
	ParentSessionID string `json:"parentSessionId"`
	ProjectID       string `json:"projectId"`
	FinalText       string `json:"finalText"`
	ErrorMessage    string `json:"errorMessage"`
}

// Pair calls POST /api/daemon/pair.
func (c *Client) Pair(req PairRequest) (*PairResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequest(
		http.MethodPost,
		c.baseURL+"/api/daemon/pair",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pair failed: %s: %s", resp.Status, string(data))
	}

	var out PairResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &out, nil
}

// RefreshToken calls POST /api/daemon/refresh-token.
func (c *Client) RefreshToken(req RefreshTokenRequest) (*RefreshTokenResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/daemon/refresh-token", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed: %s: %s", resp.Status, string(data))
	}
	var out RefreshTokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &out, nil
}

func (c *Client) ListSubagents(ctx context.Context, req ListSubagentsRequest) (*ListSubagentsResponse, error) {
	values := url.Values{}
	values.Set("machineId", req.MachineID)
	values.Set("sessionId", req.SessionID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/daemon/subagents?"+values.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("authorization", "Bearer "+req.AuthToken)
	var out ListSubagentsResponse
	if err := c.do(httpReq, http.StatusOK, &out); err != nil {
		return nil, fmt.Errorf("list subagents: %w", err)
	}
	return &out, nil
}

func (c *Client) CreateSubagentChild(ctx context.Context, req CreateSubagentChildRequest) (*CreateSubagentChildResponse, error) {
	var out CreateSubagentChildResponse
	if err := c.postBearer(ctx, "/api/daemon/subagents/child", req.AuthToken, req, &out); err != nil {
		return nil, fmt.Errorf("create subagent child: %w", err)
	}
	return &out, nil
}

func (c *Client) HeartbeatSubagentChild(ctx context.Context, req HeartbeatSubagentChildRequest) (*HeartbeatSubagentChildResponse, error) {
	var out HeartbeatSubagentChildResponse
	if err := c.postBearer(ctx, "/api/daemon/subagents/heartbeat", req.AuthToken, req, &out); err != nil {
		return nil, fmt.Errorf("heartbeat subagent child: %w", err)
	}
	return &out, nil
}

func (c *Client) FinalizeSubagentChild(ctx context.Context, req FinalizeSubagentChildRequest) (*FinalizeSubagentChildResponse, error) {
	var out FinalizeSubagentChildResponse
	if err := c.postBearer(ctx, "/api/daemon/subagents/finalize", req.AuthToken, req, &out); err != nil {
		return nil, fmt.Errorf("finalize subagent child: %w", err)
	}
	return &out, nil
}

func (c *Client) postBearer(ctx context.Context, path string, token string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("authorization", "Bearer "+token)
	httpReq.Header.Set("content-type", "application/json")
	return c.do(httpReq, http.StatusOK, out)
}

func (c *Client) do(req *http.Request, wantStatus int, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		return fmt.Errorf("%s: %s", resp.Status, string(data))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	return nil
}
