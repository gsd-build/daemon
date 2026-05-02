package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type WorkerKey struct {
	BinaryPath          string
	CWD                 string
	Model               string
	ResumeSession       string
	CustomInstructions  string
	ExtensionPath       string
	Provider            string
	SkillPaths          string
	DisableSkills       bool
	AgentToolsSocket    string
	AgentToolsTokenHash string
}

type WorkerSnapshot struct {
	SessionID  string
	Provider   string
	Model      string
	PID        int
	KeyHash    string
	State      string
	StartedAt  time.Time
	LastUsedAt time.Time
	IdleSince  *time.Time
}

func NewWorkerKey(opts Options) WorkerKey {
	skills := append([]string(nil), opts.SkillPaths...)
	sort.Strings(skills)
	key := WorkerKey{
		BinaryPath:         opts.BinaryPath,
		CWD:                opts.CWD,
		Model:              opts.Model,
		ResumeSession:      opts.ResumeSession,
		CustomInstructions: strings.TrimSpace(opts.CustomInstructions),
		ExtensionPath:      opts.ExtensionPath,
		Provider:           ProviderOrDefault(opts.Provider),
		SkillPaths:         strings.Join(skills, "\x00"),
		DisableSkills:      opts.DisableSkills,
	}
	key.AgentToolsSocket = opts.AgentToolsSocket
	if opts.AgentToolsToken != "" {
		key.AgentToolsTokenHash = hashString(opts.AgentToolsToken)
	}
	return key
}

func (k WorkerKey) Hash() string {
	data, _ := json.Marshal(k)
	return hashString(string(data))
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
