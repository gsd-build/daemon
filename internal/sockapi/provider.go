package sockapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// SessionInfo is a snapshot of one active session.
type SessionInfo struct {
	SessionID string     `json:"sessionID"`
	State     string     `json:"state"`     // "executing" or "idle"
	TaskID    string     `json:"taskID"`    // empty when idle
	StartedAt *time.Time `json:"startedAt"` // when current task started; nil when idle
	IdleSince *time.Time `json:"idleSince"` // when actor became idle; nil when executing
}

type WorkerInfo struct {
	SessionID  string     `json:"sessionID"`
	Provider   string     `json:"provider"`
	Model      string     `json:"model"`
	PID        int        `json:"pid"`
	KeyHash    string     `json:"keyHash"`
	State      string     `json:"state"`
	StartedAt  time.Time  `json:"startedAt"`
	LastUsedAt time.Time  `json:"lastUsedAt"`
	IdleSince  *time.Time `json:"idleSince"`
}

// StatusData is the full daemon status snapshot.
type StatusData struct {
	Version            string `json:"version"`
	Uptime             string `json:"uptime"`
	RelayConnected     bool   `json:"relayConnected"`
	RelayURL           string `json:"relayURL"`
	MachineID          string `json:"machineID"`
	ActiveSessions     int    `json:"activeSessions"`
	InFlightTasks      int    `json:"inFlightTasks"`
	MaxConcurrentTasks int    `json:"maxConcurrentTasks"`
	WarmWorkersEnabled bool   `json:"warmWorkersEnabled"`
	WarmWorkerIdleTTL  string `json:"warmWorkerIdleTTL"`
	WarmWorkerIdleCap  int    `json:"warmWorkerIdleCap"`
	ActiveWarmWorkers  int    `json:"activeWarmWorkers"`
	IdleWarmWorkers    int    `json:"idleWarmWorkers"`
	LogLevel           string `json:"logLevel"`
}

// HealthData is the health check response.
type HealthData struct {
	Status string `json:"status"` // "ok" or "disconnected"
}

type CreateSubagentChildRequest struct {
	ParentSessionID  string `json:"parentSessionId"`
	ParentToolCallID string `json:"parentToolCallId"`
	AgentName        string `json:"agentName"`
	Task             string `json:"task"`
}

type CreateSubagentChildResponse struct {
	ChildSessionID  string `json:"childSessionId"`
	ParentSessionID string `json:"parentSessionId"`
	ProjectID       string `json:"projectId"`
	AgentName       string `json:"agentName"`
	Model           string `json:"model"`
}

type ForwardSubagentEventRequest struct {
	ChildSessionID string          `json:"childSessionId"`
	Raw            json.RawMessage `json:"event"`
}

type RegisterSubagentProcessRequest struct {
	ChildSessionID string `json:"childSessionId"`
	PID            int    `json:"pid"`
}

type FinalizeSubagentChildRequest struct {
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
	ChildSessionID  string `json:"childSessionId"`
	ParentSessionID string `json:"parentSessionId"`
	ProjectID       string `json:"projectId"`
	FinalText       string `json:"finalText,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
}

// StatusProvider is implemented by the daemon loop to expose live state.
type StatusProvider interface {
	Health() HealthData
	Status() StatusData
	Sessions() []SessionInfo
	Workers() []WorkerInfo
}

type SubagentProvider interface {
	CreateSubagentChild(r *http.Request, req CreateSubagentChildRequest) (CreateSubagentChildResponse, error)
	RegisterSubagentProcess(r *http.Request, req RegisterSubagentProcessRequest) error
	ForwardSubagentEvent(r *http.Request, req ForwardSubagentEventRequest) error
	FinalizeSubagentChild(r *http.Request, req FinalizeSubagentChildRequest) (FinalizeSubagentChildResponse, error)
}
