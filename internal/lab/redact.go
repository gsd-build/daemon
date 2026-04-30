package lab

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type Redactor struct{}

func DefaultRedactor() Redactor {
	return Redactor{}
}

func (r Redactor) Redact(path string, value string) (string, []RedactEvent) {
	lowerPath := strings.ToLower(path)
	lowerValue := strings.ToLower(value)
	secretPath := strings.Contains(lowerPath, "authorization") ||
		strings.Contains(lowerPath, "token") ||
		strings.Contains(lowerPath, "cookie") ||
		strings.Contains(lowerPath, "secret") ||
		strings.Contains(lowerPath, "api_key")
	secretValue := strings.Contains(lowerValue, "bearer ") ||
		strings.Contains(lowerValue, "sk-")
	if !secretPath && !secretValue {
		return value, nil
	}
	sum := sha256.Sum256([]byte(value))
	return "[REDACTED]", []RedactEvent{{
		Path:   path,
		Reason: "secret-like value",
		Hash:   hex.EncodeToString(sum[:]),
	}}
}
