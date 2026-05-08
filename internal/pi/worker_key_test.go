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

func TestWorkerKeyUsesPiRustRuntimeProvider(t *testing.T) {
	key := NewWorkerKey(Options{
		BinaryPath: "/opt/bin/pi-rust",
		Provider:   "claude-cli",
	})

	if key.Runtime != RuntimeRust {
		t.Fatalf("runtime = %q, want %q", key.Runtime, RuntimeRust)
	}
	if key.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", key.Provider)
	}
}
