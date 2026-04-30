package lab

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerExportsBundle(t *testing.T) {
	store, err := NewSessionStore(SessionStoreOptions{RootDir: t.TempDir(), Config: SessionConfig{Provider: "claude-cli"}})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	_ = store.Append("task.started", map[string]any{"taskId": "t1"})
	server := NewServer(ServerOptions{Store: store, CWD: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/export", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var bundle ExportBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("parse bundle: %v", err)
	}
	if len(bundle.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(bundle.Events))
	}
}

func TestServerReturnsConfig(t *testing.T) {
	store, err := NewSessionStore(SessionStoreOptions{
		RootDir: t.TempDir(),
		Config:  SessionConfig{CWD: "/tmp/project", Provider: "claude-cli", Model: "claude-sonnet-4-6"},
	})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	server := NewServer(ServerOptions{Store: store, CWD: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/tmp/project") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestServerReadsFilesInsideCWD(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "README.md")
	if err := os.WriteFile(path, []byte("hello lab"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	store, err := NewSessionStore(SessionStoreOptions{RootDir: t.TempDir(), Config: SessionConfig{}})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	server := NewServer(ServerOptions{Store: store, CWD: cwd})
	req := httptest.NewRequest(http.MethodGet, "/api/file?path="+path, nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hello lab") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestServerReadsFilesInsideCWDWithEscapedQueryPath(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "README.md")
	if err := os.WriteFile(path, []byte("hello escaped lab"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	store, err := NewSessionStore(SessionStoreOptions{RootDir: t.TempDir(), Config: SessionConfig{}})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	server := NewServer(ServerOptions{Store: store, CWD: cwd})
	req := httptest.NewRequest(http.MethodGet, "/api/file?path=%2F"+strings.TrimPrefix(path, "/"), nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hello escaped lab") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestServerServesStaticUI(t *testing.T) {
	store, err := NewSessionStore(SessionStoreOptions{RootDir: t.TempDir(), Config: SessionConfig{}})
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	server := NewServer(ServerOptions{Store: store, CWD: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Provider Lab") {
		t.Fatalf("body missing Provider Lab: %s", rec.Body.String())
	}
}
