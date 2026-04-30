package lab

import (
	"embed"
	"encoding/json"
	"net/http"
	"path/filepath"

	daemonfs "github.com/gsd-build/daemon/internal/fs"
)

//go:embed static/*
var staticFiles embed.FS

type ServerOptions struct {
	Store *SessionStore
	Relay *LocalRelay
	CWD   string
}

type Server struct {
	mux   *http.ServeMux
	store *SessionStore
	relay *LocalRelay
	cwd   string
}

func NewServer(opts ServerOptions) *Server {
	s := &Server{
		mux:   http.NewServeMux(),
		store: opts.Store,
		relay: opts.Relay,
		cwd:   opts.CWD,
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/config", s.handleConfig)
	s.mux.HandleFunc("/api/export", s.handleExport)
	s.mux.HandleFunc("/api/scenarios", s.handleScenarios)
	s.mux.HandleFunc("/api/file", s.handleFile)
	s.mux.HandleFunc("/api/browse", s.handleBrowse)
	s.mux.HandleFunc("/api/agent-plan/", s.handlePlan)
	s.mux.Handle("/static/", http.FileServer(http.FS(staticFiles)))
	s.mux.HandleFunc("/", s.handleIndex)
	if s.relay != nil {
		s.mux.HandleFunc("/ws/daemon", s.relay.HandleDaemonWS)
		s.mux.HandleFunc("/ws/ui", s.relay.HandleUIWS)
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.Config(), nil)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	bundle, err := s.store.Export(ExportOptions{})
	writeJSON(w, bundle, err)
}

func (s *Server) handleScenarios(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, BuiltInScenarios(), nil)
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Query().Get("path"))
	content, truncated, err := daemonfs.ReadFile(path, s.cwd, daemonfs.DefaultMaxBytes)
	writeJSON(w, map[string]any{"path": path, "content": content, "truncated": truncated}, err)
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Query().Get("path"))
	if path == "." {
		path = s.cwd
	}
	entries, err := daemonfs.BrowseDir(path, s.cwd)
	writeJSON(w, map[string]any{"path": path, "entries": entries}, err)
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	_ = s.store.Append("plan.request", map[string]any{"method": r.Method, "path": r.URL.Path})
	writeJSON(w, map[string]any{"ok": true}, nil)
}

func writeJSON(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}
