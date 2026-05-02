package pi

import "testing"

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

func TestWorkerKeyIncludesBrowserGrant(t *testing.T) {
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
	}
	a := NewWorkerKey(base)

	cases := []struct {
		name string
		edit func(*Options)
	}{
		{name: "grant", edit: func(opts *Options) { opts.BrowserGrantID = "grant-2" }},
		{name: "browser", edit: func(opts *Options) { opts.BrowserID = "browser-2" }},
		{name: "session", edit: func(opts *Options) { opts.BrowserSessionID = "session-2" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			other := base
			tc.edit(&other)
			if a == NewWorkerKey(other) {
				t.Fatalf("worker key ignored browser %s", tc.name)
			}
		})
	}
}

func TestWorkerKeyIncludesAgentTools(t *testing.T) {
	base := Options{
		BinaryPath:       "/bin/pi",
		CWD:              "/repo",
		Model:            "claude-sonnet-4-6",
		ResumeSession:    "/tmp/session.jsonl",
		ExtensionPath:    "/ext/index.ts",
		Provider:         "claude-cli",
		AgentToolsSocket: "/tmp/agent-tools.sock",
		AgentToolsToken:  "token-1",
	}
	a := NewWorkerKey(base)

	otherSocket := base
	otherSocket.AgentToolsSocket = "/tmp/other.sock"
	if a == NewWorkerKey(otherSocket) {
		t.Fatal("worker key ignored agent tools socket")
	}

	otherToken := base
	otherToken.AgentToolsToken = "token-2"
	if a == NewWorkerKey(otherToken) {
		t.Fatal("worker key ignored agent tools token")
	}

	if a.AgentToolsTokenHash == "" || a.AgentToolsTokenHash == "token-1" {
		t.Fatalf("agent tool token hash = %q", a.AgentToolsTokenHash)
	}
}
