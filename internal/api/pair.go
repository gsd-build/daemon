// Package api contains HTTP clients for the GSD Cloud web app.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
