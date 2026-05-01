package sockapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// newHandler returns an http.Handler with all socket API routes registered.
func newHandler(p StatusProvider, authSecret ...string) http.Handler {
	secret := ""
	if len(authSecret) > 0 {
		secret = authSecret[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		h := p.Health()
		w.Header().Set("Content-Type", "application/json")
		if h.Status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(h)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p.Status())
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p.Sessions())
	})
	mux.HandleFunc("GET /workers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p.Workers())
	})
	mux.HandleFunc("POST /subagents/create-child", func(w http.ResponseWriter, r *http.Request) {
		provider, ok := p.(SubagentProvider)
		if !ok {
			http.Error(w, "subagent provider unavailable", http.StatusNotFound)
			return
		}
		var req CreateSubagentChildRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeInvalidSubagentJSON(w, err)
			return
		}
		if err := authorizeSubagentRequest(r, secret, "create-child", req.ParentSessionID, "", p); err != nil {
			writeSubagentResponse(w, nil, err)
			return
		}
		resp, err := provider.CreateSubagentChild(r, req)
		writeSubagentResponse(w, resp, err)
	})
	mux.HandleFunc("POST /subagents/forward-event", func(w http.ResponseWriter, r *http.Request) {
		provider, ok := p.(SubagentProvider)
		if !ok {
			http.Error(w, "subagent provider unavailable", http.StatusNotFound)
			return
		}
		var req ForwardSubagentEventRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeInvalidSubagentJSON(w, err)
			return
		}
		if err := authorizeSubagentRequest(r, secret, "forward-event", "", req.ChildSessionID, p); err != nil {
			writeSubagentResponse(w, nil, err)
			return
		}
		writeSubagentResponse(w, map[string]bool{"ok": true}, provider.ForwardSubagentEvent(r, req))
	})
	mux.HandleFunc("POST /subagents/register-process", func(w http.ResponseWriter, r *http.Request) {
		provider, ok := p.(SubagentProvider)
		if !ok {
			http.Error(w, "subagent provider unavailable", http.StatusNotFound)
			return
		}
		var req RegisterSubagentProcessRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeInvalidSubagentJSON(w, err)
			return
		}
		if err := authorizeSubagentRequest(r, secret, "register-process", "", req.ChildSessionID, p); err != nil {
			writeSubagentResponse(w, nil, err)
			return
		}
		writeSubagentResponse(w, map[string]bool{"ok": true}, provider.RegisterSubagentProcess(r, req))
	})
	mux.HandleFunc("POST /subagents/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		provider, ok := p.(SubagentProvider)
		if !ok {
			http.Error(w, "subagent provider unavailable", http.StatusNotFound)
			return
		}
		var req HeartbeatSubagentChildRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeInvalidSubagentJSON(w, err)
			return
		}
		if err := authorizeSubagentRequest(r, secret, "heartbeat", "", req.ChildSessionID, p); err != nil {
			writeSubagentResponse(w, nil, err)
			return
		}
		writeSubagentResponse(w, map[string]bool{"ok": true}, provider.HeartbeatSubagentChild(r, req))
	})
	mux.HandleFunc("POST /subagents/finalize", func(w http.ResponseWriter, r *http.Request) {
		provider, ok := p.(SubagentProvider)
		if !ok {
			http.Error(w, "subagent provider unavailable", http.StatusNotFound)
			return
		}
		var req FinalizeSubagentChildRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeInvalidSubagentJSON(w, err)
			return
		}
		if err := authorizeSubagentRequest(r, secret, "finalize", "", req.ChildSessionID, p); err != nil {
			writeSubagentResponse(w, nil, err)
			return
		}
		resp, err := provider.FinalizeSubagentChild(r, req)
		writeSubagentResponse(w, resp, err)
	})
	return mux
}

func authorizeSubagentRequest(r *http.Request, secret string, operation string, parentSessionID string, childSessionID string, p StatusProvider) error {
	var childParents SubagentChildParentProvider
	if provider, ok := p.(SubagentChildParentProvider); ok {
		childParents = provider
	}
	return verifySubagentAuthToken(secret, bearerToken(r), operation, parentSessionID, childSessionID, childParents, time.Now())
}

func writeInvalidSubagentJSON(w http.ResponseWriter, err error) {
	writeSubagentResponse(w, nil, fmt.Errorf("%w: invalid json: %v", ErrBadSubagentRequest, err))
}

func writeSubagentResponse(w http.ResponseWriter, resp any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrBadSubagentRequest) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, ErrSubagentUnauthorized) {
			status = http.StatusUnauthorized
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(resp)
}

var ErrBadSubagentRequest = errors.New("bad subagent request")
