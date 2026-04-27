package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func piSessionFileForSession(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session id is required for pi persistence")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for pi persistence: %w", err)
	}

	dir := filepath.Join(home, ".gsd-cloud", "pi-sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create pi session directory: %w", err)
	}

	return filepath.Join(dir, safePiSessionFilename(sessionID)+".jsonl"), nil
}

func safePiSessionFilename(sessionID string) string {
	var b strings.Builder
	b.Grow(len(sessionID))
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
