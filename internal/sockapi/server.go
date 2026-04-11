package sockapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Server serves the status API over a Unix domain socket.
type Server struct {
	sockPath string
	httpSrv  *http.Server
}

// NewServer creates a Server that will listen on sockPath.
func NewServer(sockPath string, provider StatusProvider) *Server {
	return &Server{
		sockPath: sockPath,
		httpSrv: &http.Server{
			Handler: newHandler(provider),
		},
	}
}

// ListenAndServe starts the Unix socket listener and serves HTTP until
// ctx is cancelled. Removes stale socket files on startup and cleans up
// on shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(s.sockPath), 0700); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}

	// Remove stale socket from a previous crash.
	os.Remove(s.sockPath)

	ln, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}

	// Set owner-only permissions.
	if err := os.Chmod(s.sockPath, 0600); err != nil {
		ln.Close()
		os.Remove(s.sockPath)
		return fmt.Errorf("chmod socket: %w", err)
	}

	// Shut down when context is cancelled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(shutdownCtx)
	}()

	err = s.httpSrv.Serve(ln)
	// Clean up socket file on exit.
	os.Remove(s.sockPath)

	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
