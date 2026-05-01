package sockapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const SubagentAuthEnv = "GSD_SUBAGENT_AUTH_TOKEN"

type SubagentAuthClaims struct {
	ParentSessionID string `json:"parentSessionId,omitempty"`
	ChildSessionID  string `json:"childSessionId,omitempty"`
	Operation       string `json:"operation"`
	ExpiresAt       int64  `json:"expiresAt"`
}

type SubagentChildParentProvider interface {
	ParentSessionForSubagentChild(childSessionID string) (string, bool)
}

var ErrSubagentUnauthorized = errors.New("subagent request unauthorized")

func NewSubagentAuthToken(secret string, claims SubagentAuthClaims) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("subagent auth secret is required")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig, nil
}

func verifySubagentAuthToken(secret string, token string, operation string, parentSessionID string, childSessionID string, childParents SubagentChildParentProvider, now time.Time) error {
	if secret == "" {
		return nil
	}
	if token == "" {
		return ErrSubagentUnauthorized
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return ErrSubagentUnauthorized
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, want) {
		return ErrSubagentUnauthorized
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrSubagentUnauthorized
	}
	var claims SubagentAuthClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ErrSubagentUnauthorized
	}
	if claims.ExpiresAt <= now.Unix() {
		return ErrSubagentUnauthorized
	}
	if claims.Operation != "*" && claims.Operation != operation {
		return ErrSubagentUnauthorized
	}
	if parentSessionID != "" && claims.ParentSessionID != "" && claims.ParentSessionID != parentSessionID {
		return ErrSubagentUnauthorized
	}
	if childSessionID != "" {
		if claims.ChildSessionID != "" && claims.ChildSessionID != childSessionID {
			return ErrSubagentUnauthorized
		}
		if claims.ParentSessionID != "" && childParents != nil {
			parent, ok := childParents.ParentSessionForSubagentChild(childSessionID)
			if !ok || parent != claims.ParentSessionID {
				return ErrSubagentUnauthorized
			}
		}
	}
	return nil
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}
