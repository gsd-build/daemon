package sockapi

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerStartAndShutdown(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

	p := &mockProvider{health: HealthData{Status: "ok"}}
	srv := NewServer(sockPath, p)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx)
	}()

	// Wait for socket file to appear (up to 2s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify socket file exists.
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("socket file did not appear: %v", err)
	}

	// Verify 0600 permissions.
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected socket permissions 0600, got %04o", perm)
	}

	// Verify we can connect.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to connect to socket: %v", err)
	}
	conn.Close()

	// Cancel context and wait for shutdown.
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ListenAndServe returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}

	// Verify socket file is removed after shutdown.
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("socket file was not removed after shutdown")
	}
}

func TestServerCleansStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")

	// Create a stale regular file at the socket path.
	if err := os.WriteFile(sockPath, []byte("stale"), 0600); err != nil {
		t.Fatalf("failed to create stale file: %v", err)
	}

	p := &mockProvider{health: HealthData{Status: "ok"}}
	srv := NewServer(sockPath, p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx)
	}()

	// Wait for socket to become connectable (up to 2s).
	var dialErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var conn net.Conn
		conn, dialErr = net.Dial("unix", sockPath)
		if dialErr == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if dialErr != nil {
		t.Fatalf("failed to connect after stale socket cleanup: %v", dialErr)
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ListenAndServe returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}
