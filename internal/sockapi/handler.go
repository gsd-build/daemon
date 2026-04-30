package sockapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// newHandler returns an http.Handler with all socket API routes registered.
func newHandler(p StatusProvider) http.Handler {
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
		writeSubagentResponse(w, map[string]bool{"ok": true}, provider.RegisterSubagentProcess(r, req))
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
		resp, err := provider.FinalizeSubagentChild(r, req)
		writeSubagentResponse(w, resp, err)
	})
	return mux
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
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(resp)
}

var ErrBadSubagentRequest = errors.New("bad subagent request")
