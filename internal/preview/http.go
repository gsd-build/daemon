package preview

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	protocol "github.com/gsd-build/protocol-go"
)

type Sender interface {
	Send(ctx context.Context, msg any) error
}

type HTTPHandler struct {
	Registry *Registry
	Sender   Sender
	Client   *http.Client
}

var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

func (h *HTTPHandler) Handle(ctx context.Context, msg *protocol.PreviewHTTPRequest) error {
	preview, ok := h.Registry.Get(msg.PreviewID)
	if !ok {
		return fmt.Errorf("preview not active")
	}
	streamCtx, cancel, ok := h.Registry.RegisterStream(msg.PreviewID, msg.StreamID)
	if !ok {
		return fmt.Errorf("stream not registered")
	}
	defer cancel()
	defer h.Registry.UnregisterStream(msg.PreviewID, msg.StreamID)

	if msg.Path == "" || !strings.HasPrefix(msg.Path, "/") || strings.HasPrefix(msg.Path, "//") {
		return fmt.Errorf("preview path must be origin-form")
	}
	if parsed, err := url.Parse(msg.Path); err != nil || parsed.IsAbs() || parsed.Host != "" {
		return fmt.Errorf("preview path must be origin-form")
	}

	reqCtx, reqCancel := context.WithCancel(ctx)
	defer reqCancel()
	go func() {
		select {
		case <-streamCtx.Done():
			reqCancel()
		case <-reqCtx.Done():
		}
	}()

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := h.doTargetRequest(reqCtx, client, preview.Target, msg)
	if err != nil {
		return fmt.Errorf("target request: %w", err)
	}
	defer resp.Body.Close()

	blockedResponseHeaders := blockedHeaderNames(resp.Header)
	headers := map[string][]string{}
	for key, values := range resp.Header {
		if _, skip := blockedResponseHeaders[strings.ToLower(key)]; skip {
			continue
		}
		headers[strings.ToLower(key)] = append([]string(nil), values...)
	}
	if err := h.Sender.Send(ctx, &protocol.PreviewHTTPResponseHead{
		Type:       protocol.MsgTypePreviewHTTPResponseHead,
		RequestID:  msg.RequestID,
		StreamID:   msg.StreamID,
		PreviewID:  msg.PreviewID,
		StatusCode: resp.StatusCode,
		Headers:    headers,
	}); err != nil {
		return err
	}

	buf := make([]byte, DefaultChunkBytes)
	var sequence int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			sequence++
			if err := h.Sender.Send(ctx, &protocol.PreviewStreamChunk{
				Type:       protocol.MsgTypePreviewStreamChunk,
				StreamID:   msg.StreamID,
				Sequence:   sequence,
				BodyBase64: base64.StdEncoding.EncodeToString(buf[:n]),
				Final:      false,
			}); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			sequence++
			return h.Sender.Send(ctx, &protocol.PreviewStreamChunk{
				Type:     protocol.MsgTypePreviewStreamChunk,
				StreamID: msg.StreamID,
				Sequence: sequence,
				Final:    true,
			})
		}
		if readErr != nil {
			return fmt.Errorf("read target body: %w", readErr)
		}
	}
}

func (h *HTTPHandler) doTargetRequest(ctx context.Context, client *http.Client, target Target, msg *protocol.PreviewHTTPRequest) (*http.Response, error) {
	var lastErr error
	for _, addr := range target.Addrs() {
		localURL := "http://" + addr + msg.Path
		req, err := http.NewRequestWithContext(ctx, msg.Method, localURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		copyRequestHeaders(req.Header, msg.Headers)
		applyLocalTargetHeaders(req, msg.Headers, msg.PreviewID, addr)

		resp, err := client.Do(req)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func copyRequestHeaders(dst http.Header, src map[string][]string) {
	blocked := blockedHeaderNames(src)
	for key, values := range src {
		if _, skip := blocked[strings.ToLower(key)]; skip {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func blockedHeaderNames(headers map[string][]string) map[string]struct{} {
	blocked := make(map[string]struct{}, len(hopByHopHeaders))
	for key := range hopByHopHeaders {
		blocked[key] = struct{}{}
	}
	blocked["host"] = struct{}{}
	for key, values := range headers {
		if !strings.EqualFold(key, "connection") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				name := strings.TrimSpace(token)
				if name == "" {
					continue
				}
				blocked[strings.ToLower(http.CanonicalHeaderKey(name))] = struct{}{}
			}
		}
	}
	return blocked
}

func firstHeader(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func applyLocalTargetHeaders(req *http.Request, source map[string][]string, previewID string, targetAuthority string) {
	req.Host = targetAuthority
	if previewHost := firstHeader(source, "host"); previewHost != "" {
		req.Header.Set("X-Forwarded-Host", previewHost)
	}
	req.Header.Set("X-Gsd-Preview-Id", previewID)
}
