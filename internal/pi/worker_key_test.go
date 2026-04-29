package pi

import (
	"testing"

	protocol "github.com/gsd-build/protocol-go"
)

func TestNewWorkerKeyDefaultsProviderAndSortsSkills(t *testing.T) {
	a := NewWorkerKey(Options{
		BinaryPath:    "/bin/pi",
		CWD:           "/repo",
		Model:         "claude-sonnet-4-6",
		ResumeSession: "/tmp/session.jsonl",
		ExtensionPath: "/ext/index.ts",
		SkillPaths:    []string{"/skills/b", "/skills/a"},
	})
	b := NewWorkerKey(Options{
		BinaryPath:    "/bin/pi",
		CWD:           "/repo",
		Model:         "claude-sonnet-4-6",
		ResumeSession: "/tmp/session.jsonl",
		ExtensionPath: "/ext/index.ts",
		Provider:      "claude-cli",
		SkillPaths:    []string{"/skills/a", "/skills/b"},
	})

	if a != b {
		t.Fatalf("keys differ after default provider and skill sorting:\na=%+v\nb=%+v", a, b)
	}
}

func TestWorkerKeyIncludesBrowserAndPlanCapability(t *testing.T) {
	base := Options{
		BinaryPath:       "/bin/pi",
		CWD:              "/repo",
		Model:            "claude-sonnet-4-6",
		ResumeSession:    "/tmp/session.jsonl",
		ExtensionPath:    "/ext/index.ts",
		Provider:         "claude-cli",
		BrowserGrantID:   "grant-1",
		BrowserID:        "browser-1",
		BrowserSessionID: "session-1",
		PlanCapability: &protocol.PlanCapability{
			Token:      "token-1",
			APIBaseURL: "https://api.gsd.build",
			ExpiresAt:  "2026-04-29T20:00:00Z",
		},
	}
	a := NewWorkerKey(base)

	withoutBrowser := base
	withoutBrowser.BrowserGrantID = ""
	withoutBrowser.BrowserID = ""
	if a == NewWorkerKey(withoutBrowser) {
		t.Fatal("worker key ignored browser grant")
	}

	otherPlan := base
	otherPlan.PlanCapability = &protocol.PlanCapability{
		Token:      "token-2",
		APIBaseURL: "https://api.gsd.build",
		ExpiresAt:  "2026-04-29T20:00:00Z",
	}
	if a == NewWorkerKey(otherPlan) {
		t.Fatal("worker key ignored plan capability")
	}
}

func TestWorkerKeyRedactsPlanTokenInHash(t *testing.T) {
	key := NewWorkerKey(Options{
		BinaryPath:    "/bin/pi",
		CWD:           "/repo",
		Model:         "claude-sonnet-4-6",
		ResumeSession: "/tmp/session.jsonl",
		ExtensionPath: "/ext/index.ts",
		Provider:      "claude-cli",
		PlanCapability: &protocol.PlanCapability{
			Token:      "secret-token",
			APIBaseURL: "https://api.gsd.build",
			ExpiresAt:  "2026-04-29T20:00:00Z",
		},
	})

	if key.Hash() == "" {
		t.Fatal("expected stable non-empty hash")
	}
	if key.Hash() == "secret-token" {
		t.Fatal("hash exposed raw plan token")
	}
}
