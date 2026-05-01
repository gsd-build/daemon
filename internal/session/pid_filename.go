package session

import (
	"crypto/sha256"
	"encoding/base64"
)

func pidFilenameForTask(taskID string) string {
	sum := sha256.Sum256([]byte(taskID))
	return "task-" + base64.RawURLEncoding.EncodeToString(sum[:]) + ".pid"
}
