package sockapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// unixHTTPClient returns an http.Client that dials the given Unix socket.
func unixHTTPClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}

// QueryHealth checks daemon health via the Unix socket.
func QueryHealth(sockPath string) (*HealthData, error) {
	client := unixHTTPClient(sockPath)
	resp, err := client.Get("http://daemon/health")
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	var data HealthData
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode health: %w", err)
	}
	return &data, nil
}

// QueryStatus fetches the full daemon status via the Unix socket.
func QueryStatus(sockPath string) (*StatusData, error) {
	client := unixHTTPClient(sockPath)
	resp, err := client.Get("http://daemon/status")
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	var data StatusData
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	return &data, nil
}

// QuerySessions fetches the active session list via the Unix socket.
func QuerySessions(sockPath string) ([]SessionInfo, error) {
	client := unixHTTPClient(sockPath)
	resp, err := client.Get("http://daemon/sessions")
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	var data []SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	return data, nil
}

// IsDaemonRunning checks whether the daemon is reachable on the socket.
func IsDaemonRunning(sockPath string) bool {
	conn, err := net.DialTimeout("unix", sockPath, 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
