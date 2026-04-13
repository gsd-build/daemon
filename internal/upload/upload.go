// Package upload provides an HTTP client for uploading images to the relay's
// /internal/upload endpoint, authenticated with the daemon's machine token.
package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// imageExtensions maps lowercase extensions to true for file types the relay
// can process as images.
var imageExtensions = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".svg":  true,
	".webp": true,
}

// IsImageFile returns true if the file path has a recognised image extension.
func IsImageFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return imageExtensions[ext]
}

// Client uploads images to the relay HTTP endpoint.
type Client struct {
	baseURL    string // e.g. "https://relay.gsd.build"
	machineID  string
	authToken  string
	httpClient *http.Client
}

// NewClient creates an upload client. relayWSURL is the daemon's configured
// WebSocket URL (e.g. "wss://relay.gsd.build/ws/daemon"); it is converted to
// an HTTPS base URL automatically.
func NewClient(relayWSURL, machineID, authToken string) *Client {
	return &Client{
		baseURL:   wsToHTTPBase(relayWSURL),
		machineID: machineID,
		authToken: authToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Upload sends the image data to the relay and returns the public URL.
func (c *Client) Upload(ctx context.Context, filename string, data []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("write form file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart: %w", err)
	}

	url := c.baseURL + "/internal/upload"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	req.Header.Set("X-Machine-Id", c.machineID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("upload failed: status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return result.URL, nil
}

// wsToHTTPBase converts a WebSocket relay URL to an HTTP base URL.
// "wss://relay.gsd.build/ws/daemon?machineId=..." → "https://relay.gsd.build"
// "ws://localhost:8080/ws/daemon" → "http://localhost:8080"
func wsToHTTPBase(wsURL string) string {
	u := wsURL

	// Strip query string
	if idx := strings.Index(u, "?"); idx >= 0 {
		u = u[:idx]
	}

	// Strip path
	// Find the third slash (after scheme://)
	schemeEnd := strings.Index(u, "://")
	if schemeEnd >= 0 {
		rest := u[schemeEnd+3:]
		if slashIdx := strings.Index(rest, "/"); slashIdx >= 0 {
			u = u[:schemeEnd+3+slashIdx]
		}
	}

	// Convert scheme
	if strings.HasPrefix(u, "wss://") {
		u = "https://" + u[6:]
	} else if strings.HasPrefix(u, "ws://") {
		u = "http://" + u[5:]
	}

	return u
}
