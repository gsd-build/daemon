package pi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/claude"
	protocol "github.com/gsd-build/protocol-go"
)

func TestTerminateProcessGroupAndWaitEscalates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group signals are unix-specific")
	}

	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}

	start := time.Now()
	err := terminateProcessGroupAndWait(cmd, cmd.Process.Pid, nil, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected killed process error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("terminate took %s", elapsed)
	}
}

func TestPiExitError_DetectsMissingExtensionDeps(t *testing.T) {
	cases := []struct {
		name       string
		stderr     string
		wantHint   bool
		wantSubstr string
	}{
		{
			name:       "Cannot find module signature",
			stderr:     `Failed to load extension: Cannot find module '@anthropic-ai/claude-agent-sdk'`,
			wantHint:   true,
			wantSubstr: "self-heal will run npm ci",
		},
		{
			name:       "Unknown provider claude-cli signature",
			stderr:     `Unknown provider "claude-cli". Use --list-models to see available providers/models.`,
			wantHint:   true,
			wantSubstr: "self-heal will run npm ci",
		},
		{
			name:       "Generic non-zero exit",
			stderr:     "some unrelated error from pi",
			wantHint:   false,
			wantSubstr: "pi exited with code 1",
		},
		{
			name:       "Empty stderr",
			stderr:     "",
			wantHint:   false,
			wantSubstr: "no stderr",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := piExitError(1, c.stderr)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantSubstr) {
				t.Errorf("err=%q, want substring %q", err.Error(), c.wantSubstr)
			}
		})
	}
}

func TestExecutorRequiresExistingExtension(t *testing.T) {
	exec := NewExecutor(Options{
		BinaryPath:    "definitely-not-a-real-pi-binary",
		CWD:           t.TempDir(),
		ExtensionPath: t.TempDir() + "/missing/index.ts",
		Prompt:        "hello",
	})

	err := exec.Run(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected missing extension error")
	}
	if !strings.Contains(err.Error(), "pi extension not found") {
		t.Fatalf("expected missing extension error, got %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestExecutorPassesCustomInstructionsAsAppendSystemPrompt(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "pi.args")
	fakePi := writeFakePi(t, `
: > "`+argsFile+`"
for arg in "$@"; do
  printf '%s\000' "$arg" >> "`+argsFile+`"
done
IFS= read -r prompt_frame || true
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
`)
	extensionPath := filepath.Join(t.TempDir(), "index.ts")
	if err := os.WriteFile(extensionPath, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write extension: %v", err)
	}

	exec := NewExecutor(Options{
		BinaryPath:         fakePi,
		CWD:                t.TempDir(),
		ExtensionPath:      extensionPath,
		Provider:           "claude-cli",
		Prompt:             "hello",
		CustomInstructions: "  Always talk like a pirate.  ",
	})

	if err := exec.Run(context.Background(), func(claude.Event) error { return nil }, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	args := strings.Split(string(data), "\x00")
	if len(args) > 0 && args[len(args)-1] == "" {
		args = args[:len(args)-1]
	}
	flag := -1
	for i, arg := range args {
		if arg == "--append-system-prompt" {
			flag = i
			break
		}
	}
	if flag < 0 || flag+1 >= len(args) {
		t.Fatalf("pi args missing --append-system-prompt value: %v", args)
	}
	systemPrompt := args[flag+1]
	if !strings.Contains(systemPrompt, "Always talk like a pirate.") {
		t.Fatalf("append system prompt missing custom instructions: %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "<runtime_context>") {
		t.Fatalf("append system prompt missing runtime context: %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "Provider: claude-cli") {
		t.Fatalf("append system prompt missing provider: %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "Model: default") {
		t.Fatalf("append system prompt missing default model: %q", systemPrompt)
	}
}

func TestRuntimeIdentityPromptIncludesTaskFacts(t *testing.T) {
	now := time.Date(2026, 4, 29, 15, 41, 0, 0, time.FixedZone("America/Costa_Rica", -6*60*60))
	prompt := runtimeIdentityPrompt(Options{
		Provider: "openrouter",
		Model:    "z-ai/glm-4.7-flash",
		CWD:      "/tmp/gsd\nbad",
	}, now)

	for _, want := range []string{
		"<runtime_context>",
		"Provider: openrouter",
		"Model: z-ai/glm-4.7-flash",
		"Local OS/arch: " + runtime.GOOS + "/" + runtime.GOARCH,
		"Working directory: /tmp/gsd bad",
		"Local date: 2026-04-29",
		"Local UTC offset: -06:00",
		"Local timezone name: America/Costa_Rica",
		"</runtime_context>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("runtime identity prompt missing %q: %q", want, prompt)
		}
	}
}

func TestExecutorPassesNoSkillsWhenDisabled(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "pi.args")
	fakePi := writeFakePi(t, `
: > "`+argsFile+`"
for arg in "$@"; do
  printf '%s\000' "$arg" >> "`+argsFile+`"
done
IFS= read -r prompt_frame || true
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
`)
	extensionPath := filepath.Join(t.TempDir(), "index.ts")
	if err := os.WriteFile(extensionPath, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write extension: %v", err)
	}

	exec := NewExecutor(Options{
		BinaryPath:    fakePi,
		CWD:           t.TempDir(),
		ExtensionPath: extensionPath,
		Provider:      "claude-cli",
		Prompt:        "hello",
		DisableSkills: true,
		SkillPaths:    []string{"/tmp/should-not-be-used/SKILL.md"},
	})

	if err := exec.Run(context.Background(), func(claude.Event) error { return nil }, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	if !strings.Contains(string(data), "--no-skills\x00") {
		t.Fatalf("pi args missing --no-skills: %q", string(data))
	}
	if strings.Contains(string(data), "--skill\x00") {
		t.Fatalf("pi args should not include --skill when disabled: %q", string(data))
	}
}

func TestExecutorPassesPlanCapabilityEnv(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "pi.env")
	fakePi := writeFakePi(t, `
{
  printf 'GSD_PLAN_API_BASE_URL=%s\n' "${GSD_PLAN_API_BASE_URL:-}"
  printf 'GSD_PLAN_CAPABILITY_TOKEN=%s\n' "${GSD_PLAN_CAPABILITY_TOKEN:-}"
  printf 'GSD_PLAN_CAPABILITY_EXPIRES_AT=%s\n' "${GSD_PLAN_CAPABILITY_EXPIRES_AT:-}"
} > "`+envFile+`"
IFS= read -r prompt_frame || true
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
`)
	extensionPath := filepath.Join(t.TempDir(), "index.ts")
	if err := os.WriteFile(extensionPath, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write extension: %v", err)
	}

	exec := NewExecutor(Options{
		BinaryPath:    fakePi,
		CWD:           t.TempDir(),
		ExtensionPath: extensionPath,
		Provider:      "claude-cli",
		Prompt:        "hello",
		PlanCapability: &protocol.PlanCapability{
			APIBaseURL: "https://app.test",
			Token:      "gsd_plan_test_secret",
			ExpiresAt:  "2026-04-28T22:30:00Z",
		},
	})

	if err := exec.Run(context.Background(), func(claude.Event) error { return nil }, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "GSD_PLAN_API_BASE_URL=https://app.test\n") {
		t.Fatalf("env missing api base url: %s", got)
	}
	if !strings.Contains(got, "GSD_PLAN_CAPABILITY_TOKEN=gsd_plan_test_secret\n") {
		t.Fatalf("env missing capability token: %s", got)
	}
	if !strings.Contains(got, "GSD_PLAN_CAPABILITY_EXPIRES_AT=2026-04-28T22:30:00Z\n") {
		t.Fatalf("env missing expires at: %s", got)
	}
}

func TestExecutorPassesAgentToolsEnv(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"GSD_AGENT_TOOLS_SOCKET=/tmp/stale.sock",
		"GSD_AGENT_TOOLS_TOKEN=stale-token",
		"GSD_SESSION_ID=stale-session",
		"GSD_CHANNEL_ID=stale-channel",
		"GSD_TASK_ID=stale-task",
	}
	env := processEnv(context.Background(), base, Options{
		Provider:         "claude-cli",
		AgentToolsSocket: "/tmp/agent-tools.sock",
		AgentToolsToken:  "fresh-token",
		SessionID:        "sess-1",
		ChannelID:        "chan-1",
		TaskID:           "task-1",
	})
	got := strings.Join(env, "\n")
	for _, want := range []string{
		"GSD_AGENT_TOOLS_SOCKET=/tmp/agent-tools.sock",
		"GSD_AGENT_TOOLS_TOKEN=fresh-token",
		"GSD_SESSION_ID=sess-1",
		"GSD_CHANNEL_ID=chan-1",
		"GSD_TASK_ID=task-1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("env missing %q: %s", want, got)
		}
	}
	for _, stale := range []string{
		"/tmp/stale.sock",
		"stale-token",
		"stale-session",
		"stale-channel",
		"stale-task",
	} {
		if strings.Contains(got, stale) {
			t.Fatalf("env kept stale agent tool value %q: %s", stale, got)
		}
	}
}

func TestExecutorControlsWarmClaudeSDKEnv(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"GSD_WARM_CLAUDE_SDK=1",
	}

	cold := processEnv(context.Background(), base, Options{Provider: "claude-cli"})
	for _, entry := range cold {
		if strings.HasPrefix(entry, "GSD_WARM_CLAUDE_SDK=") {
			t.Fatalf("cold env should not contain warm SDK flag: %v", cold)
		}
	}

	warm := processEnv(context.Background(), base, Options{Provider: "claude-cli", WarmClaudeSDK: true})
	found := false
	for _, entry := range warm {
		if entry == "GSD_WARM_CLAUDE_SDK=1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warm env missing SDK flag: %v", warm)
	}
}

func TestExecutorUsesServiceManagerOpenRouterEnv(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "pi.env")
	fakePi := writeFakePi(t, `
{
  printf 'OPENROUTER_API_KEY=%s\n' "${OPENROUTER_API_KEY:-}"
} > "`+envFile+`"
IFS= read -r prompt_frame || true
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
`)
	extensionPath := filepath.Join(t.TempDir(), "index.ts")
	if err := os.WriteFile(extensionPath, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write extension: %v", err)
	}

	t.Setenv(openRouterAPIKeyEnv, " ")
	oldLookup := lookupServiceManagerEnv
	lookupServiceManagerEnv = func(_ context.Context, key string) string {
		if key == openRouterAPIKeyEnv {
			return "sk-or-service-manager"
		}
		return ""
	}
	t.Cleanup(func() { lookupServiceManagerEnv = oldLookup })

	exec := NewExecutor(Options{
		BinaryPath:    fakePi,
		CWD:           t.TempDir(),
		ExtensionPath: extensionPath,
		Provider:      "openrouter",
		Prompt:        "hello",
	})

	if err := exec.Run(context.Background(), func(claude.Event) error { return nil }, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if got := string(data); !strings.Contains(got, "OPENROUTER_API_KEY=sk-or-service-manager\n") {
		t.Fatalf("env missing service manager key: %s", got)
	}
}

func TestExecutorMakesServiceManagerOpenRouterEnvAvailableToSubagents(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "pi.env")
	fakePi := writeFakePi(t, `
{
  printf 'OPENROUTER_API_KEY=%s\n' "${OPENROUTER_API_KEY:-}"
} > "`+envFile+`"
IFS= read -r prompt_frame || true
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
`)
	extensionPath := filepath.Join(t.TempDir(), "index.ts")
	if err := os.WriteFile(extensionPath, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write extension: %v", err)
	}

	t.Setenv(openRouterAPIKeyEnv, " ")
	oldLookup := lookupServiceManagerEnv
	lookupServiceManagerEnv = func(_ context.Context, key string) string {
		if key == openRouterAPIKeyEnv {
			return "sk-or-service-manager"
		}
		return ""
	}
	t.Cleanup(func() { lookupServiceManagerEnv = oldLookup })

	exec := NewExecutor(Options{
		BinaryPath:    fakePi,
		CWD:           t.TempDir(),
		ExtensionPath: extensionPath,
		Provider:      "claude-cli",
		Prompt:        "hello",
	})

	if err := exec.Run(context.Background(), func(claude.Event) error { return nil }, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	if got := string(data); !strings.Contains(got, "OPENROUTER_API_KEY=sk-or-service-manager\n") {
		t.Fatalf("env missing service manager key for child subagents: %s", got)
	}
}

func TestExecutorReportsToolExecutionStart(t *testing.T) {
	t.Run("snake case", func(t *testing.T) {
		var got ToolExecutionStart
		exec := Executor{
			OnToolExecutionStart: func(event ToolExecutionStart) {
				got = event
			},
		}

		raw := json.RawMessage(`{"type":"tool_execution_start","tool_call_id":"toolu_123","tool_name":"ask_user_questions","args":{"questions":[{"id":"scope","question":"Pick","options":[{"label":"A"}]}]}}`)
		if err := exec.handlePiEventForTest(context.Background(), raw, func(_ claude.Event) error { return nil }); err != nil {
			t.Fatalf("handlePiEventForTest: %v", err)
		}
		assertToolExecutionStart(t, got)
	})

	t.Run("camel case", func(t *testing.T) {
		var got ToolExecutionStart
		exec := Executor{
			OnToolExecutionStart: func(event ToolExecutionStart) {
				got = event
			},
		}

		raw := json.RawMessage(`{"type":"tool_execution_start","toolCallId":"toolu_123","toolName":"ask_user_questions","args":{"questions":[{"id":"scope","question":"Pick","options":[{"label":"A"}]}]}}`)
		if err := exec.handlePiEventForTest(context.Background(), raw, func(_ claude.Event) error { return nil }); err != nil {
			t.Fatalf("handlePiEventForTest: %v", err)
		}
		assertToolExecutionStart(t, got)
	})
}

func assertToolExecutionStart(t *testing.T, got ToolExecutionStart) {
	t.Helper()
	if got.ToolCallID != "toolu_123" {
		t.Fatalf("ToolCallID = %q, want toolu_123", got.ToolCallID)
	}
	if got.ToolName != "ask_user_questions" {
		t.Fatalf("ToolName = %q, want ask_user_questions", got.ToolName)
	}
	questions, ok := got.Args["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("questions = %#v, want one question", got.Args["questions"])
	}
	question, ok := questions[0].(map[string]any)
	if !ok {
		t.Fatalf("question = %#v, want object", questions[0])
	}
	if question["id"] != "scope" {
		t.Fatalf("question id = %#v, want scope", question["id"])
	}
}

func TestNotifyToolExecutionEnd_ParsesWriteResult(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"tool_execution_end",
		"toolCallId":"call_abc",
		"toolName":"write",
		"result":{"content":[{"type":"text","text":"Successfully wrote 12 bytes to a.txt"}]},
		"isError":false
	}`)
	var got *ToolExecutionEnd
	notifyToolExecutionEnd(raw, func(ev ToolExecutionEnd) { got = &ev })
	if got == nil {
		t.Fatalf("callback not fired")
	}
	if got.ToolCallID != "call_abc" || got.ToolName != "write" || got.IsError {
		t.Fatalf("unexpected payload: %+v", got)
	}
	if got.Result == nil {
		t.Fatalf("expected non-nil Result map")
	}
}

func TestNotifyToolExecutionEnd_PreservesEditFirstChangedLine(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"tool_execution_end",
		"toolCallId":"call_xyz",
		"toolName":"edit",
		"result":{"content":[{"type":"text","text":"ok"}],"details":{"firstChangedLine":42,"diff":"-1 a\n+1 b"}},
		"isError":false
	}`)
	var got *ToolExecutionEnd
	notifyToolExecutionEnd(raw, func(ev ToolExecutionEnd) { got = &ev })
	if got == nil {
		t.Fatalf("callback not fired")
	}
	details, _ := got.Result["details"].(map[string]any)
	fcl, _ := details["firstChangedLine"].(float64)
	if int(fcl) != 42 {
		t.Fatalf("firstChangedLine: got %v want 42", fcl)
	}
}

func TestNotifyToolExecutionEnd_NilCallbackIsNoop(t *testing.T) {
	raw := json.RawMessage(`{"type":"tool_execution_end","toolName":"write"}`)
	notifyToolExecutionEnd(raw, nil) // must not panic
}

func TestNotifyToolExecutionEnd_IgnoresWrongType(t *testing.T) {
	raw := json.RawMessage(`{"type":"something_else"}`)
	called := false
	notifyToolExecutionEnd(raw, func(ev ToolExecutionEnd) { called = true })
	if called {
		t.Fatalf("callback should not fire on wrong type")
	}
}

func TestStreamPiEvents_FiresOnToolExecutionEnd(t *testing.T) {
	stream := strings.NewReader(`{"type":"tool_execution_end","toolCallId":"c1","toolName":"edit","result":{"details":{"firstChangedLine":7}},"isError":false}` + "\n")

	var got *ToolExecutionEnd
	state := &translatorState{}
	err := streamPiEvents(
		context.Background(),
		stream,
		io.Discard,
		func(_ claude.Event) error { return nil },
		nil,
		nil,
		func(ev ToolExecutionEnd) { got = &ev },
		make(chan struct{}, 1),
		false,
		state,
		time.Now(),
	)
	if err != nil && err != io.EOF {
		t.Fatalf("streamPiEvents error: %v", err)
	}
	if got == nil {
		t.Fatalf("OnToolExecutionEnd callback never fired")
	}
	if got.ToolCallID != "c1" || got.ToolName != "edit" {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestStreamPiEventsForwardsToolExecutionUpdate(t *testing.T) {
	stream := strings.NewReader(`{"type":"tool_execution_update","toolCallId":"c1","toolName":"shell_exec","partialResult":{"content":[{"type":"text","text":"line 1"},{"type":"text","text":"line 2"}],"details":{"jobId":"job-1","status":"running"}}}` + "\n")

	var events []claude.Event
	err := streamPiEvents(
		context.Background(),
		stream,
		io.Discard,
		func(event claude.Event) error {
			events = append(events, event)
			return nil
		},
		nil,
		nil,
		nil,
		make(chan struct{}, 1),
		true,
		&translatorState{sessionID: "sess-1"},
		time.Now(),
	)
	if err != nil && err != io.EOF {
		t.Fatalf("streamPiEvents error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	var top struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Event     struct {
			Type       string         `json:"type"`
			ToolCallID string         `json:"tool_call_id"`
			ToolName   string         `json:"tool_name"`
			Text       string         `json:"text"`
			Details    map[string]any `json:"details"`
		} `json:"event"`
	}
	if err := json.Unmarshal(events[0].Raw, &top); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if top.Type != "stream_event" || top.SessionID != "sess-1" {
		t.Fatalf("top = %#v", top)
	}
	if top.Event.Type != "tool_execution_update" ||
		top.Event.ToolCallID != "c1" ||
		top.Event.ToolName != "shell_exec" ||
		top.Event.Text != "line 1line 2" ||
		top.Event.Details["jobId"] != "job-1" ||
		top.Event.Details["status"] != "running" {
		t.Fatalf("event = %#v", top.Event)
	}
}

func TestStreamPiEvents_ReturnsAgentEndError(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[],"provider":"openrouter","model":"z-ai/glm-4.7-flash","stopReason":"error","errorMessage":"401 Missing Authentication header"}}`,
		`{"type":"agent_end","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"assistant","content":[],"provider":"openrouter","model":"z-ai/glm-4.7-flash","stopReason":"error","errorMessage":"401 Missing Authentication header"}]}`,
		"",
	}, "\n"))

	events := []string{}
	err := streamPiEvents(
		context.Background(),
		stream,
		io.Discard,
		func(e claude.Event) error {
			events = append(events, e.Type)
			return nil
		},
		nil,
		nil,
		nil,
		make(chan struct{}, 1),
		true,
		&translatorState{},
		time.Now(),
	)
	if err == nil {
		t.Fatal("expected agent_end error")
	}
	if !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("error = %q, want OPENROUTER_API_KEY hint", err.Error())
	}
	for _, eventType := range events {
		if eventType == "result" {
			t.Fatal("error agent_end emitted result event")
		}
	}
}
