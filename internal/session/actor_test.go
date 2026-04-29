package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gsd-build/daemon/internal/pi"
	protocol "github.com/gsd-build/protocol-go"
)

func writeFakePi(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "fake-pi")
	script := `#!/bin/sh
if [ -n "$FAKE_PI_ARGS_FILE" ]; then
  : > "$FAKE_PI_ARGS_FILE"
  for arg in "$@"; do
    printf '%s\000' "$arg" >> "$FAKE_PI_ARGS_FILE"
  done
fi
IFS= read -r prompt_frame || true
if [ -n "$FAKE_PI_PROMPT_FILE" ]; then
  printf '%s\n' "$prompt_frame" > "$FAKE_PI_PROMPT_FILE"
fi
if [ -n "$FAKE_PI_ENV_FILE" ]; then
  {
    printf 'GSD_PLAN_API_BASE_URL=%s\n' "${GSD_PLAN_API_BASE_URL:-}"
    printf 'GSD_PLAN_CAPABILITY_TOKEN=%s\n' "${GSD_PLAN_CAPABILITY_TOKEN:-}"
    printf 'GSD_PLAN_CAPABILITY_EXPIRES_AT=%s\n' "${GSD_PLAN_CAPABILITY_EXPIRES_AT:-}"
  } > "$FAKE_PI_ENV_FILE"
fi
if [ -n "$FAKE_PI_SLEEP" ]; then
  sleep "$FAKE_PI_SLEEP"
fi
if [ "$FAKE_PI_INVALID_AGENT_END" = "1" ]; then
  printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":"not-a-number"}}]}'
  exit 0
fi
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"user","content":[{"type":"text","text":"remember this"}]},{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
	`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return path
}

func writeFakePiExtension(t *testing.T) string {
	t.Helper()
	extensionPath := filepath.Join(t.TempDir(), "index.ts")
	if err := os.WriteFile(extensionPath, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write fake pi extension: %v", err)
	}
	return extensionPath
}

func testPiOptions(t *testing.T, opts Options) Options {
	t.Helper()
	if opts.CWD == "" {
		opts.CWD = t.TempDir()
	}
	if opts.PiBinaryPath == "" {
		opts.PiBinaryPath = writeFakePi(t)
	}
	if opts.PiExtensionPath == "" {
		opts.PiExtensionPath = writeFakePiExtension(t)
	}
	return opts
}

func readNulArgsFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read nul args file: %v", err)
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func argIndex(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}

func argValues(args []string, flag string) []string {
	values := make([]string, 0)
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			values = append(values, args[i+1])
		}
	}
	return values
}

func TestNormalizePiModelStripsBracketSuffix(t *testing.T) {
	tests := map[string]string{
		"claude-opus-4-6[1m]":      "claude-opus-4-6",
		" claude-opus-4-6[1m] ":    "claude-opus-4-6",
		"claude-sonnet-4-6":        "claude-sonnet-4-6",
		"claude-sonnet-4-6:high":   "claude-sonnet-4-6:high",
		"anthropic/[custom-model]": "anthropic/[custom-model]",
	}

	for input, want := range tests {
		if got := normalizePiModel(input); got != want {
			t.Fatalf("normalizePiModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPiSessionFileForSessionUsesGSDSessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := piSessionFileForSession("sess-persist-pi")
	if err != nil {
		t.Fatalf("piSessionFileForSession: %v", err)
	}

	want := filepath.Join(home, ".gsd-cloud", "pi-sessions", "sess-persist-pi.jsonl")
	if got != want {
		t.Fatalf("piSessionFileForSession = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Dir(got)); err != nil {
		t.Fatalf("expected pi session directory: %v", err)
	}
}

func TestActorPiExecutorUsesPersistentSessionFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	argsFile := filepath.Join(t.TempDir(), "pi.args")
	t.Setenv("FAKE_PI_ARGS_FILE", argsFile)

	extensionPath := filepath.Join(t.TempDir(), "index.ts")
	if err := os.WriteFile(extensionPath, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write fake extension: %v", err)
	}

	actor, err := NewActor(Options{
		SessionID:       "sess-persist-pi",
		CWD:             t.TempDir(),
		Relay:           newFakeRelay(),
		Model:           "claude-sonnet-4-6",
		PiBinaryPath:    writeFakePi(t),
		PiExtensionPath: extensionPath,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.runPiExecutor(context.Background(), context.Background(), &taskContext{
		TaskID:    "task-persist-pi",
		ChannelID: "ch-persist-pi",
		Engine:    "pi",
		Model:     "claude-opus-4-6[1m]",
	}, "remember this")
	if err != nil {
		t.Fatalf("runPiExecutor: %v", err)
	}

	args := readNulArgsFile(t, argsFile)
	if argIndex(args, "--no-session") >= 0 {
		t.Fatalf("pi args include --no-session: %v", args)
	}
	sessionFlag := argIndex(args, "--session")
	if sessionFlag < 0 {
		t.Fatalf("pi args missing --session: %v", args)
	}
	if sessionFlag+1 >= len(args) {
		t.Fatalf("pi args missing --session value: %v", args)
	}

	wantSessionFile := filepath.Join(home, ".gsd-cloud", "pi-sessions", "sess-persist-pi.jsonl")
	if args[sessionFlag+1] != wantSessionFile {
		t.Fatalf("pi session file = %q, want %q", args[sessionFlag+1], wantSessionFile)
	}
	modelFlag := argIndex(args, "--model")
	if modelFlag < 0 || modelFlag+1 >= len(args) {
		t.Fatalf("pi args missing --model value: %v", args)
	}
	if args[modelFlag+1] != "claude-opus-4-6" {
		t.Fatalf("pi model = %q, want claude-opus-4-6", args[modelFlag+1])
	}
	if argIndex(args, "--no-skills") >= 0 {
		t.Fatalf("pi args disable skills: %v", args)
	}
	if argIndex(args, "--no-extensions") < 0 {
		t.Fatalf("pi args missing --no-extensions: %v", args)
	}
	if argIndex(args, "--no-prompt-templates") < 0 {
		t.Fatalf("pi args missing --no-prompt-templates: %v", args)
	}
	if got := actor.currentPiModel(); got != "claude-opus-4-6" {
		t.Fatalf("current pi model = %q", got)
	}
}

func TestActorPiExecutorPassesTaskProvider(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "pi.args")
	t.Setenv("FAKE_PI_ARGS_FILE", argsFile)

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-provider-codex",
		Relay:     newFakeRelay(),
		Model:     "claude-opus-4-6",
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.runPiExecutor(context.Background(), context.Background(), &taskContext{
		TaskID:    "task-provider-codex",
		ChannelID: "ch-provider-codex",
		Engine:    "pi",
		Provider:  "codex-appserver",
		Model:     "gpt-5.5",
	}, "ask a question")
	if err != nil {
		t.Fatalf("runPiExecutor: %v", err)
	}

	args := readNulArgsFile(t, argsFile)
	providerFlag := argIndex(args, "--provider")
	if providerFlag < 0 || providerFlag+1 >= len(args) {
		t.Fatalf("pi args missing --provider value: %v", args)
	}
	if args[providerFlag+1] != "codex-appserver" {
		t.Fatalf("provider = %q, want codex-appserver", args[providerFlag+1])
	}
	modelFlag := argIndex(args, "--model")
	if modelFlag < 0 || modelFlag+1 >= len(args) {
		t.Fatalf("pi args missing --model value: %v", args)
	}
	if args[modelFlag+1] != "gpt-5.5" {
		t.Fatalf("model = %q, want gpt-5.5", args[modelFlag+1])
	}
}

func TestActorPiExecutorDefaultsEmptyProviderToClaudeCli(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "pi.args")
	t.Setenv("FAKE_PI_ARGS_FILE", argsFile)

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-provider-default",
		Relay:     newFakeRelay(),
		Model:     "claude-opus-4-6",
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.runPiExecutor(context.Background(), context.Background(), &taskContext{
		TaskID:    "task-provider-default",
		ChannelID: "ch-provider-default",
		Engine:    "pi",
		Model:     "claude-sonnet-4-6",
	}, "hello")
	if err != nil {
		t.Fatalf("runPiExecutor: %v", err)
	}

	args := readNulArgsFile(t, argsFile)
	providerFlag := argIndex(args, "--provider")
	if providerFlag < 0 || providerFlag+1 >= len(args) {
		t.Fatalf("pi args missing --provider value: %v", args)
	}
	if args[providerFlag+1] != "claude-cli" {
		t.Fatalf("provider = %q, want claude-cli", args[providerFlag+1])
	}
}

func TestActorPiExecutorPassesCustomInstructions(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "pi.args")
	t.Setenv("FAKE_PI_ARGS_FILE", argsFile)

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-custom-instructions",
		Relay:     newFakeRelay(),
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.runPiExecutor(context.Background(), context.Background(), &taskContext{
		TaskID:             "task-custom-instructions",
		ChannelID:          "ch-custom-instructions",
		Engine:             "pi",
		CustomInstructions: "Always talk like a pirate.",
	}, "remember this")
	if err != nil {
		t.Fatalf("runPiExecutor: %v", err)
	}

	args := readNulArgsFile(t, argsFile)
	flag := argIndex(args, "--append-system-prompt")
	if flag < 0 || flag+1 >= len(args) {
		t.Fatalf("pi args missing --append-system-prompt value: %v", args)
	}
	if args[flag+1] != "Always talk like a pirate." {
		t.Fatalf("append system prompt = %q", args[flag+1])
	}
}

func TestActorPiExecutorPassesPlanCapability(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "pi.env")
	t.Setenv("FAKE_PI_ENV_FILE", envFile)

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-plan-capability",
		Relay:     newFakeRelay(),
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.runPiExecutor(context.Background(), context.Background(), &taskContext{
		TaskID:    "task-plan-capability",
		ChannelID: "ch-plan-capability",
		Engine:    "pi",
		PlanCapability: &protocol.PlanCapability{
			APIBaseURL: "https://app.test",
			Token:      "gsd_plan_actor_secret",
			ExpiresAt:  "2026-04-28T22:30:00Z",
		},
	}, "remember this")
	if err != nil {
		t.Fatalf("runPiExecutor: %v", err)
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "GSD_PLAN_API_BASE_URL=https://app.test\n") {
		t.Fatalf("env missing api base url: %s", got)
	}
	if !strings.Contains(got, "GSD_PLAN_CAPABILITY_TOKEN=gsd_plan_actor_secret\n") {
		t.Fatalf("env missing capability token: %s", got)
	}
	if !strings.Contains(got, "GSD_PLAN_CAPABILITY_EXPIRES_AT=2026-04-28T22:30:00Z\n") {
		t.Fatalf("env missing expires at: %s", got)
	}
}

func TestActorPiExecutorPassesReferencedClaudeSkillPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectDir := filepath.Join(home, "repo", "app")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(home, "repo", ".claude", "skills", "project-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: project-skill\ndescription: Project skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", "home-skill"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "skills", "home-skill", "SKILL.md"), []byte("---\nname: home-skill\ndescription: Home skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	argsFile := filepath.Join(t.TempDir(), "pi.args")
	t.Setenv("FAKE_PI_ARGS_FILE", argsFile)

	extensionPath := filepath.Join(t.TempDir(), "index.ts")
	if err := os.WriteFile(extensionPath, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write fake extension: %v", err)
	}

	actor, err := NewActor(Options{
		SessionID:       "sess-pi-skills",
		CWD:             projectDir,
		Relay:           newFakeRelay(),
		PiBinaryPath:    writeFakePi(t),
		PiExtensionPath: extensionPath,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.runPiExecutor(context.Background(), context.Background(), &taskContext{
		TaskID:         "task-pi-skills",
		ChannelID:      "ch-pi-skills",
		Engine:         "pi",
		OriginalPrompt: "/skill:project-skill use the project skill",
	}, "/skill:project-skill use the project skill")
	if err != nil {
		t.Fatalf("runPiExecutor: %v", err)
	}

	args := readNulArgsFile(t, argsFile)
	skillPaths := argValues(args, "--skill")
	if len(skillPaths) != 1 || skillPaths[0] != skillPath {
		t.Fatalf("pi skill paths = %+v, want only %q", skillPaths, skillPath)
	}
}

func TestActorPiExecutorSkipsSkillsWithoutPromptReference(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectDir := filepath.Join(home, "repo", "app")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", "home-skill"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "skills", "home-skill", "SKILL.md"), []byte("---\nname: home-skill\ndescription: Home skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	argsFile := filepath.Join(t.TempDir(), "pi.args")
	t.Setenv("FAKE_PI_ARGS_FILE", argsFile)

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-pi-no-skills",
		CWD:       projectDir,
		Relay:     newFakeRelay(),
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.runPiExecutor(context.Background(), context.Background(), &taskContext{
		TaskID:         "task-pi-no-skills",
		ChannelID:      "ch-pi-no-skills",
		Engine:         "pi",
		OriginalPrompt: "hello",
	}, "<context>\n/skill:home-skill appears in injected context\n</context>\nhello")
	if err != nil {
		t.Fatalf("runPiExecutor: %v", err)
	}

	args := readNulArgsFile(t, argsFile)
	if skillPaths := argValues(args, "--skill"); len(skillPaths) != 0 {
		t.Fatalf("pi skill paths = %+v, want none", skillPaths)
	}
}

func TestHandleResultDoesNotPersistPiSyntheticSession(t *testing.T) {
	relay := newFakeRelay()
	actor := &Actor{
		opts: Options{
			SessionID: "sess-pi-result",
			Relay:     relay,
		},
	}

	err := actor.handleResult(context.Background(), &taskContext{
		TaskID:    "task-pi-result",
		ChannelID: "ch-pi-result",
		Engine:    "pi",
	}, json.RawMessage(`{
		"session_id": "pi-rpc-task-pi-result",
		"total_cost_usd": 0,
		"duration_ms": 12,
		"usage": {"input_tokens": 1, "output_tokens": 2}
	}`))
	if err != nil {
		t.Fatalf("handleResult: %v", err)
	}
	if got := actor.GetClaudeSessionID(); got != "" {
		t.Fatalf("expected no persisted Claude session for pi result, got %q", got)
	}

	frames := relay.GetFrames()
	var complete *protocol.TaskComplete
	for _, frame := range frames {
		if tc, ok := frame.(*protocol.TaskComplete); ok {
			complete = tc
			break
		}
	}
	if complete == nil {
		t.Fatal("expected TaskComplete")
	}
	if complete.ClaudeSessionID != "" {
		t.Fatalf("expected empty ClaudeSessionID for pi result, got %q", complete.ClaudeSessionID)
	}
}

type fakeRelay struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames []any
}

func newFakeRelay() *fakeRelay {
	r := &fakeRelay{}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *fakeRelay) Send(ctx context.Context, msg any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, msg)
	r.cond.Broadcast()
	return nil
}

func (r *fakeRelay) GetFrames() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]any, len(r.frames))
	copy(out, r.frames)
	return out
}

func (r *fakeRelay) waitFor(t *testing.T, timeout time.Duration, predicate func([]any) bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)

	r.mu.Lock()
	defer r.mu.Unlock()

	if predicate(r.frames) {
		return true
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-time.After(timeout):
			r.mu.Lock()
			r.cond.Broadcast()
			r.mu.Unlock()
		case <-stop:
		}
	}()

	for !predicate(r.frames) {
		if time.Now().After(deadline) {
			return false
		}
		r.cond.Wait()
	}
	return true
}

type blockingUploader struct {
	started chan context.Context
	done    chan struct{}
}

func newBlockingUploader() *blockingUploader {
	return &blockingUploader{
		started: make(chan context.Context, 1),
		done:    make(chan struct{}),
	}
}

func (u *blockingUploader) Upload(ctx context.Context, filename string, data []byte) (string, error) {
	select {
	case u.started <- ctx:
	default:
	}
	<-ctx.Done()
	close(u.done)
	return "", ctx.Err()
}

func (r *fakeRelay) waitForTaskComplete(t *testing.T, timeout time.Duration) bool {
	return r.waitFor(t, timeout, func(frames []any) bool {
		for _, f := range frames {
			if _, ok := f.(*protocol.TaskComplete); ok {
				return true
			}
		}
		return false
	})
}

func (r *fakeRelay) waitForType(t *testing.T, msgType string, timeout time.Duration) bool {
	return r.waitFor(t, timeout, func(frames []any) bool {
		for _, f := range frames {
			switch v := f.(type) {
			case *protocol.TaskStarted:
				if v.Type == msgType {
					return true
				}
			case *protocol.TaskCancelled:
				if v.Type == msgType {
					return true
				}
			case *protocol.TaskComplete:
				if v.Type == msgType {
					return true
				}
			case *protocol.TaskError:
				if v.Type == msgType {
					return true
				}
			}
		}
		return false
	})
}

func TestCancelTask_ActorStaysAlive(t *testing.T) {
	relay := newFakeRelay()

	t.Setenv("FAKE_PI_SLEEP", "5")
	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-cancel",
		Relay:     relay,
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- actor.Run(ctx) }()

	// Send first task
	if err := actor.SendTask(protocol.Task{
		TaskID:    "t1",
		SessionID: "sess-cancel",
		ChannelID: "ch1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatal(err)
	}

	// Wait for task to start (taskStarted frame)
	if !relay.waitForType(t, "taskStarted", 5*time.Second) {
		t.Fatal("timed out waiting for taskStarted")
	}

	// Cancel the task
	actor.CancelTask()

	// Wait for taskCancelled frame
	if !relay.waitForType(t, "taskCancelled", 5*time.Second) {
		t.Fatal("timed out waiting for taskCancelled")
	}

	// Verify the actor is still alive by sending a second task
	if err := actor.SendTask(protocol.Task{
		TaskID:    "t2",
		SessionID: "sess-cancel",
		ChannelID: "ch2",
		Prompt:    "world",
	}); err != nil {
		t.Fatalf("second task should succeed: %v", err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for second task to complete")
	}

	// Verify actor.Run() hasn't returned (actor is still alive)
	select {
	case err := <-done:
		t.Fatalf("actor.Run() should not have returned, got: %v", err)
	default:
		// good — still running
	}
}

func TestActorHappyPath(t *testing.T) {
	relay := newFakeRelay()

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-1",
		Relay:     relay,
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-1",
		SessionID: "sess-1",
		ChannelID: "ch-1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete frame")
	}
	_ = actor.Stop()

	frames := relay.GetFrames()
	var streamFrames []*protocol.Stream
	for _, f := range frames {
		if s, ok := f.(*protocol.Stream); ok {
			streamFrames = append(streamFrames, s)
		}
	}
	if len(streamFrames) < 2 {
		t.Fatalf("expected at least 2 stream frames, got %d", len(streamFrames))
	}

	var lastSeq int64
	for i, s := range streamFrames {
		if s.SequenceNumber <= lastSeq {
			t.Errorf("non-monotonic seq at %d: %d", i, s.SequenceNumber)
		}
		lastSeq = s.SequenceNumber
	}

	var completes []*protocol.TaskComplete
	for _, f := range frames {
		if tc, ok := f.(*protocol.TaskComplete); ok {
			completes = append(completes, tc)
		}
	}
	if len(completes) != 1 {
		t.Fatalf("expected 1 taskComplete, got %d", len(completes))
	}
	if completes[0].ClaudeSessionID != "" {
		t.Errorf("expected empty claudeSessionId, got %s", completes[0].ClaudeSessionID)
	}
}

func TestActorIncludesContextRefsInExecutorPrompt(t *testing.T) {
	relay := newFakeRelay()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	promptFile := filepath.Join(t.TempDir(), "pi-prompt.json")
	t.Setenv("FAKE_PI_PROMPT_FILE", promptFile)

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-context",
		CWD:       root,
		Relay:     relay,
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-context",
		SessionID: "sess-context",
		ChannelID: "ch-context",
		Prompt:    "inspect this",
		CWD:       root,
		ContextRefs: []protocol.ContextRef{
			{Kind: "file", Path: "README.md", Name: "README.md"},
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete frame")
	}

	promptBytes, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("read prompt file: %v", err)
	}
	var promptFrame struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(promptBytes, &promptFrame); err != nil {
		t.Fatalf("unmarshal prompt frame: %v", err)
	}
	prompt := promptFrame.Message
	if !strings.Contains(prompt, "<attached_context>") {
		t.Fatalf("prompt missing attached context:\n%s", prompt)
	}
	if !strings.Contains(prompt, `<file path="README.md" bytes=6>`) {
		t.Fatalf("prompt missing file context:\n%s", prompt)
	}
	if !strings.Contains(prompt, "hello\n") || !strings.Contains(prompt, "inspect this") {
		t.Fatalf("prompt missing body or original prompt:\n%s", prompt)
	}
}

func TestActorPropagatesRequestIDToLifecycleFrames(t *testing.T) {
	relay := newFakeRelay()

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-request-id",
		Relay:     relay,
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:      "task-request-id",
		SessionID:   "sess-request-id",
		ChannelID:   "ch-request-id",
		Prompt:      "hello",
		RequestID:   "11111111-2222-4333-8444-555555555555",
		Traceparent: "00-11111111222243338444555555555555-aaaaaaaaaaaaaaaa-01",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete frame")
	}

	var started *protocol.TaskStarted
	var completed *protocol.TaskComplete
	for _, frame := range relay.GetFrames() {
		switch v := frame.(type) {
		case *protocol.TaskStarted:
			started = v
		case *protocol.TaskComplete:
			completed = v
		}
	}
	if started == nil || completed == nil {
		t.Fatal("expected both TaskStarted and TaskComplete frames")
	}
	if started.RequestID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("taskStarted requestId = %q", started.RequestID)
	}
	if completed.RequestID != "11111111-2222-4333-8444-555555555555" {
		t.Fatalf("taskComplete requestId = %q", completed.RequestID)
	}
}

func TestActorMalformedFinalResultEmitsTaskError(t *testing.T) {
	relay := newFakeRelay()

	t.Setenv("FAKE_PI_INVALID_AGENT_END", "1")

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-invalid-result",
		Relay:     relay,
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "task-invalid-result",
		SessionID: "sess-invalid-result",
		ChannelID: "ch-invalid-result",
		Prompt:    "hello",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	gotError := relay.waitFor(t, 10*time.Second, func(frames []any) bool {
		for _, f := range frames {
			if te, ok := f.(*protocol.TaskError); ok {
				return te.TaskID == "task-invalid-result"
			}
		}
		return false
	})
	if !gotError {
		t.Fatal("timed out waiting for TaskError frame")
	}

	for _, frame := range relay.GetFrames() {
		if tc, ok := frame.(*protocol.TaskComplete); ok && tc.TaskID == "task-invalid-result" {
			t.Fatal("expected malformed final result to avoid TaskComplete")
		}
	}
}

func TestMaybeUploadImagesUsesTaskContext(t *testing.T) {
	tmpDir := t.TempDir()
	imagePath := filepath.Join(tmpDir, "sample.png")
	if err := os.WriteFile(imagePath, []byte("png"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	uploader := newBlockingUploader()
	actor, err := NewActor(Options{
		SessionID: "sess-image-context",
		CWD:       tmpDir,
		Relay:     newFakeRelay(),
		Uploader:  uploader,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	raw, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{
				{
					"type": "tool_use",
					"name": "Read",
					"id":   "toolu_img_1",
					"input": map[string]any{
						"file_path": imagePath,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal raw event: %v", err)
	}

	taskCtx, cancel := context.WithCancel(context.Background())
	actor.maybeUploadImages(taskCtx, raw, "ch-image-context", 1)

	select {
	case <-uploader.started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected upload to start")
	}

	cancel()

	select {
	case <-uploader.done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected upload goroutine to stop after task context cancellation")
	}
}

func TestActorSendTaskWhenBusy(t *testing.T) {
	relay := newFakeRelay()
	a, err := NewActor(Options{
		SessionID: "s-1",
		Relay:     relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer a.Stop()

	_ = a.SendTask(protocol.Task{TaskID: "t1", Prompt: "first"})

	err = a.SendTask(protocol.Task{TaskID: "t2", Prompt: "second"})
	if err == nil {
		t.Fatal("expected error when task channel full")
	}
}

func TestActorTaskTimeout(t *testing.T) {
	relay := newFakeRelay()

	t.Setenv("FAKE_PI_SLEEP", "10")

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-timeout",
		Relay:     relay,
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	defer actor.Stop()

	// Set a very short timeout for testing
	actor.taskTimeout = 1 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t-timeout",
		SessionID: "sess-timeout",
		ChannelID: "ch1",
		Prompt:    "slow task",
	}); err != nil {
		t.Fatal(err)
	}

	// Should get a TaskError with timeout message
	gotError := relay.waitFor(t, 10*time.Second, func(frames []any) bool {
		for _, f := range frames {
			if te, ok := f.(*protocol.TaskError); ok {
				if te.TaskID == "t-timeout" {
					return true
				}
			}
		}
		return false
	})
	if !gotError {
		t.Fatal("expected TaskError for timeout")
	}

	// Verify the actor is still alive
	select {
	case err := <-done:
		t.Fatalf("actor.Run() should not have returned, got: %v", err)
	default:
		// good
	}
}

func TestActorLastActiveAt(t *testing.T) {
	relay := newFakeRelay()

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-active",
		Relay:     relay,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer actor.Stop()

	// LastActiveAt should be set to creation time
	initial := actor.LastActiveAt()
	if initial.IsZero() {
		t.Error("expected non-zero initial lastActiveAt")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t1",
		SessionID: "sess-active",
		ChannelID: "ch1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatal(err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete")
	}

	updated := actor.LastActiveAt()
	if !updated.After(initial) {
		t.Errorf("expected lastActiveAt to advance: initial=%v updated=%v", initial, updated)
	}
}

func TestActorWritesPIDFile(t *testing.T) {
	relay := newFakeRelay()
	pidDir := t.TempDir()

	actor, err := NewActor(testPiOptions(t, Options{
		SessionID: "sess-pid",
		Relay:     relay,
	}))
	if err != nil {
		t.Fatal(err)
	}
	actor.pidDir = pidDir
	defer actor.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = actor.Run(ctx) }()

	if err := actor.SendTask(protocol.Task{
		TaskID:    "t1",
		SessionID: "sess-pid",
		ChannelID: "ch1",
		Prompt:    "hello",
	}); err != nil {
		t.Fatal(err)
	}

	if !relay.waitForTaskComplete(t, 10*time.Second) {
		t.Fatal("timed out waiting for TaskComplete")
	}

	// After completion, PID file should be cleaned up
	entries, err := os.ReadDir(pidDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected PID file to be cleaned up, found %d files", len(entries))
	}
}

func TestActorInfoExecutingState(t *testing.T) {
	relay := newFakeRelay()
	actor, err := NewActor(Options{
		SessionID: "sess-info-exec",
		CWD:       "/tmp",
		Relay:     relay,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate an in-flight task by setting fields directly.
	actor.taskMu.Lock()
	actor.taskID = "task-42"
	now := time.Now()
	actor.taskStartedAt = &now
	actor.idleSince = nil
	actor.taskMu.Unlock()

	info := actor.Info()
	if info.State != "executing" {
		t.Errorf("expected executing, got %s", info.State)
	}
	if info.TaskID != "task-42" {
		t.Errorf("expected task-42, got %s", info.TaskID)
	}
	if info.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
	if info.IdleSince != nil {
		t.Error("expected IdleSince to be nil")
	}
}

func decodeFileActivity(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal file_activity: %v", err)
	}
	return out
}

func collectFileActivityFrames(frames []any) []*protocol.Stream {
	var out []*protocol.Stream
	for _, f := range frames {
		s, ok := f.(*protocol.Stream)
		if !ok {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(s.Event, &probe); err != nil {
			continue
		}
		if probe.Type == "file_activity" {
			out = append(out, s)
		}
	}
	return out
}

func TestCapturePiToolStart_DoesNotEmitOnFileToolStart(t *testing.T) {
	relay := newFakeRelay()
	a := &Actor{opts: Options{SessionID: "sess-x", CWD: "/cwd", Relay: relay}}
	coord := &structuredQuestionCoordinator{}

	a.capturePiToolStart(coord)(pi.ToolExecutionStart{
		ToolCallID: "tc-1",
		ToolName:   "write",
		Args:       map[string]any{"path": "src/foo.ts"},
	})

	if got := collectFileActivityFrames(relay.GetFrames()); len(got) != 0 {
		t.Fatalf("expected no file_activity frames on start, got %d", len(got))
	}
	if _, ok := a.pendingFileToolStarts.Load("tc-1"); !ok {
		t.Fatal("expected pendingFileToolStarts to record write start")
	}
}

func TestCapturePiToolEnd_EmitsFileActivityForWrite(t *testing.T) {
	relay := newFakeRelay()
	var recorded []string
	a := &Actor{opts: Options{
		SessionID: "sess-fa",
		CWD:       "/work/repo",
		Relay:     relay,
		RecordTouchedFile: func(channelID, cwd, path string) {
			recorded = append(recorded, channelID+"|"+cwd+"|"+path)
		},
	}}
	coord := &structuredQuestionCoordinator{}

	a.capturePiToolStart(coord)(pi.ToolExecutionStart{
		ToolCallID: "tc-write",
		ToolName:   "write",
		Args:       map[string]any{"path": "src/new.ts"},
	})

	before := time.Now().UnixMilli()
	a.capturePiToolEnd("ch-fa")(pi.ToolExecutionEnd{
		ToolCallID: "tc-write",
		ToolName:   "write",
		Result:     map[string]any{},
		IsError:    false,
	})

	frames := collectFileActivityFrames(relay.GetFrames())
	if len(frames) != 1 {
		t.Fatalf("expected exactly 1 file_activity frame, got %d", len(frames))
	}
	frame := frames[0]
	if frame.SessionID != "sess-fa" {
		t.Errorf("sessionID = %q", frame.SessionID)
	}
	if frame.ChannelID != "ch-fa" {
		t.Errorf("channelID = %q", frame.ChannelID)
	}
	if frame.SequenceNumber <= 0 {
		t.Errorf("expected positive sequence, got %d", frame.SequenceNumber)
	}

	payload := decodeFileActivity(t, frame.Event)
	if payload["type"] != "file_activity" {
		t.Errorf("type = %v", payload["type"])
	}
	if payload["op"] != "write" {
		t.Errorf("op = %v", payload["op"])
	}
	if payload["path"] != "src/new.ts" {
		t.Errorf("path = %v", payload["path"])
	}
	if payload["cwd"] != "/work/repo" {
		t.Errorf("cwd = %v", payload["cwd"])
	}
	if v, _ := payload["firstChangedLine"].(float64); v != 0 {
		t.Errorf("firstChangedLine = %v, want 0", payload["firstChangedLine"])
	}
	if payload["toolCallId"] != "tc-write" {
		t.Errorf("toolCallId = %v", payload["toolCallId"])
	}
	if len(recorded) != 1 || recorded[0] != "ch-fa|/work/repo|src/new.ts" {
		t.Fatalf("recorded touched files = %v", recorded)
	}
	ts, _ := payload["ts"].(float64)
	if int64(ts) < before {
		t.Errorf("ts = %v, expected >= %d", payload["ts"], before)
	}
}

func TestCapturePiToolEnd_RecordsSuccessfulReadWithoutFileActivity(t *testing.T) {
	relay := newFakeRelay()
	var recorded []string
	a := &Actor{opts: Options{
		SessionID: "sess-read",
		CWD:       "/work/repo",
		Relay:     relay,
		RecordTouchedFile: func(channelID, cwd, path string) {
			recorded = append(recorded, channelID+"|"+cwd+"|"+path)
		},
	}}
	coord := &structuredQuestionCoordinator{}

	a.capturePiToolStart(coord)(pi.ToolExecutionStart{
		ToolCallID: "tc-read",
		ToolName:   "read",
		Args:       map[string]any{"path": "../skills/SKILL.md"},
	})

	a.capturePiToolEnd("ch-read")(pi.ToolExecutionEnd{
		ToolCallID: "tc-read",
		ToolName:   "read",
		Result:     map[string]any{},
		IsError:    false,
	})

	if got := collectFileActivityFrames(relay.GetFrames()); len(got) != 0 {
		t.Fatalf("expected zero file_activity frames, got %d", len(got))
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded touched files = %v, want exactly one", recorded)
	}
	if recorded[0] != "ch-read|/work/repo|../skills/SKILL.md" {
		t.Fatalf("recorded touched file = %q", recorded[0])
	}
}

func TestCapturePiToolEnd_ExtractsFirstChangedLineForEdit(t *testing.T) {
	relay := newFakeRelay()
	a := &Actor{opts: Options{SessionID: "sess-edit", CWD: "/work", Relay: relay}}
	coord := &structuredQuestionCoordinator{}

	a.capturePiToolStart(coord)(pi.ToolExecutionStart{
		ToolCallID: "tc-edit",
		ToolName:   "edit",
		Args:       map[string]any{"path": "src/old.ts"},
	})

	a.capturePiToolEnd("ch-edit")(pi.ToolExecutionEnd{
		ToolCallID: "tc-edit",
		ToolName:   "edit",
		Result: map[string]any{
			"details": map[string]any{
				"firstChangedLine": float64(42),
			},
		},
		IsError: false,
	})

	frames := collectFileActivityFrames(relay.GetFrames())
	if len(frames) != 1 {
		t.Fatalf("expected 1 file_activity frame, got %d", len(frames))
	}
	payload := decodeFileActivity(t, frames[0].Event)
	if payload["op"] != "edit" {
		t.Errorf("op = %v", payload["op"])
	}
	if v, _ := payload["firstChangedLine"].(float64); int(v) != 42 {
		t.Errorf("firstChangedLine = %v, want 42", payload["firstChangedLine"])
	}
}

func TestCapturePiToolEnd_IgnoresErrorAndNonFileTools(t *testing.T) {
	relay := newFakeRelay()
	a := &Actor{opts: Options{SessionID: "sess-i", CWD: "/work", Relay: relay}}
	coord := &structuredQuestionCoordinator{}

	// Bash tool end — never recorded as a file start, never emitted.
	a.capturePiToolEnd("ch-i")(pi.ToolExecutionEnd{
		ToolCallID: "tc-bash",
		ToolName:   "bash",
		Result:     map[string]any{},
	})

	// Write start recorded but end is errored — must NOT emit.
	a.capturePiToolStart(coord)(pi.ToolExecutionStart{
		ToolCallID: "tc-err",
		ToolName:   "write",
		Args:       map[string]any{"path": "src/err.ts"},
	})
	a.capturePiToolEnd("ch-i")(pi.ToolExecutionEnd{
		ToolCallID: "tc-err",
		ToolName:   "write",
		Result:     map[string]any{},
		IsError:    true,
	})

	// Write start with empty path — must NOT emit.
	a.capturePiToolStart(coord)(pi.ToolExecutionStart{
		ToolCallID: "tc-empty",
		ToolName:   "write",
		Args:       map[string]any{"path": ""},
	})
	a.capturePiToolEnd("ch-i")(pi.ToolExecutionEnd{
		ToolCallID: "tc-empty",
		ToolName:   "write",
		Result:     map[string]any{},
	})

	if got := collectFileActivityFrames(relay.GetFrames()); len(got) != 0 {
		t.Fatalf("expected zero file_activity frames, got %d", len(got))
	}
}

func TestCapturePiToolEnd_PendingMapIsClearedAfterEnd(t *testing.T) {
	relay := newFakeRelay()
	a := &Actor{opts: Options{SessionID: "sess-c", CWD: "/work", Relay: relay}}
	coord := &structuredQuestionCoordinator{}

	a.capturePiToolStart(coord)(pi.ToolExecutionStart{
		ToolCallID: "tc-clean",
		ToolName:   "write",
		Args:       map[string]any{"path": "src/c.ts"},
	})
	if _, ok := a.pendingFileToolStarts.Load("tc-clean"); !ok {
		t.Fatal("expected pending entry after start")
	}

	a.capturePiToolEnd("ch-c")(pi.ToolExecutionEnd{
		ToolCallID: "tc-clean",
		ToolName:   "write",
		Result:     map[string]any{},
	})

	if _, ok := a.pendingFileToolStarts.Load("tc-clean"); ok {
		t.Fatal("expected pending entry to be cleared after end")
	}
}

func TestPendingFileToolStarts_SweepDropsStaleEntries(t *testing.T) {
	a := &Actor{opts: Options{SessionID: "s1", CWD: "/wd"}}

	a.pendingFileToolStarts.Store("old", pendingFileTool{
		toolName:  "write",
		args:      map[string]any{"path": "a.txt"},
		startedAt: time.Now().Add(-2 * time.Minute),
	})
	a.pendingFileToolStarts.Store("new", pendingFileTool{
		toolName:  "write",
		args:      map[string]any{"path": "b.txt"},
		startedAt: time.Now(),
	})

	a.sweepStalePendingFileTools(60 * time.Second)

	if _, ok := a.pendingFileToolStarts.Load("old"); ok {
		t.Fatalf("stale entry should have been swept")
	}
	if _, ok := a.pendingFileToolStarts.Load("new"); !ok {
		t.Fatalf("fresh entry should have been kept")
	}
}

func TestActorInfoIdleState(t *testing.T) {
	relay := newFakeRelay()
	actor, err := NewActor(Options{
		SessionID: "sess-info-idle",
		CWD:       "/tmp",
		Relay:     relay,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Actor starts idle — set idleSince.
	actor.taskMu.Lock()
	idleTime := time.Now().Add(-10 * time.Minute)
	actor.idleSince = &idleTime
	actor.taskMu.Unlock()

	info := actor.Info()
	if info.State != "idle" {
		t.Errorf("expected idle, got %s", info.State)
	}
	if info.TaskID != "" {
		t.Errorf("expected empty taskID, got %s", info.TaskID)
	}
	if info.IdleSince == nil {
		t.Error("expected IdleSince to be set")
	}
}
