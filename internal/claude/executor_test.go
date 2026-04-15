package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func buildFakeClaude(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	daemonDir := filepath.Join(filepath.Dir(thisFile), "..", "..")
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fake-claude")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/fake-claude")
	cmd.Dir = daemonDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake-claude: %v\n%s", err, out)
	}
	return binPath
}

func readArgsFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	var argv []string
	if err := json.Unmarshal(data, &argv); err != nil {
		t.Fatalf("unmarshal args file: %v", err)
	}
	return argv
}

func TestExecutorRoundTrip(t *testing.T) {
	binPath := buildFakeClaude(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := NewExecutor(Options{
		BinaryPath:     binPath,
		CWD:            t.TempDir(),
		Model:          "test-model",
		Effort:         "max",
		PermissionMode: "acceptEdits",
		Prompt:         "hello",
	})

	var (
		mu     sync.Mutex
		events []Event
	)
	err := e.Run(ctx, func(ev Event) error {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}

	last := events[len(events)-1]
	if last.Type != "result" {
		t.Errorf("expected last event type=result, got %s", last.Type)
	}

	var payload map[string]any
	_ = json.Unmarshal(last.Raw, &payload)
	if payload["session_id"] != "fake-session-123" {
		t.Errorf("expected session_id=fake-session-123, got %v", payload["session_id"])
	}
}

func TestExecutorResumeFlag(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const resumeID = "test-claude-session-abc"
	e := NewExecutor(Options{
		BinaryPath:    binPath,
		CWD:           t.TempDir(),
		ResumeSession: resumeID,
		Prompt:        "test prompt",
		Env:           []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	_ = e.Run(ctx, func(_ Event) error { return nil })

	argv := readArgsFile(t, argsFile)
	found := false
	for i, arg := range argv {
		if arg == "--resume" && i+1 < len(argv) && argv[i+1] == resumeID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --resume %s in argv, got: %v", resumeID, argv)
	}
}

func TestExecutorAppendSystemPromptFlag(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const systemPrompt = "Always reply with quack."
	e := NewExecutor(Options{
		BinaryPath:   binPath,
		CWD:          t.TempDir(),
		SystemPrompt: systemPrompt,
		Prompt:       "test prompt",
		Env:          []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	_ = e.Run(ctx, func(_ Event) error { return nil })

	argv := readArgsFile(t, argsFile)
	found := false
	for i, arg := range argv {
		if arg == "--append-system-prompt" && i+1 < len(argv) && argv[i+1] == systemPrompt {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --append-system-prompt %q in argv, got: %v", systemPrompt, argv)
	}
}

func TestExecutorNoResumeFlag(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Prompt:     "test prompt",
		Env:        []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	_ = e.Run(ctx, func(_ Event) error { return nil })

	argv := readArgsFile(t, argsFile)
	for _, arg := range argv {
		if arg == "--resume" {
			t.Errorf("expected no --resume in argv, got: %v", argv)
			break
		}
	}
}

func TestExecutorPromptInArgs(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const prompt = "Fix the login bug"
	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Prompt:     prompt,
		Env:        []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	_ = e.Run(ctx, func(_ Event) error { return nil })

	argv := readArgsFile(t, argsFile)
	if len(argv) == 0 {
		t.Fatal("empty argv")
	}
	last := argv[len(argv)-1]
	if last != prompt {
		t.Errorf("expected last arg to be prompt %q, got %q", prompt, last)
	}
}

func TestExecutorAllowedTools(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := NewExecutor(Options{
		BinaryPath:   binPath,
		CWD:          t.TempDir(),
		Prompt:       "test",
		AllowedTools: []string{"Write", "Bash"},
		Env:          []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	_ = e.Run(ctx, func(_ Event) error { return nil })

	argv := readArgsFile(t, argsFile)
	found := false
	for i, arg := range argv {
		if arg == "--allowedTools" && i+1 < len(argv) {
			val := argv[i+1]
			if val == "Write,Bash" || val == "Bash,Write" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected --allowedTools with comma-joined tools, got: %v", argv)
	}
}

func TestExecutorStderrCapturedOnCrash(t *testing.T) {
	binPath := buildFakeClaude(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const stderrMsg = "simulated claude failure: invalid API key"
	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Prompt:     "test",
		Env: []string{
			"FAKE_CLAUDE_STDERR=" + stderrMsg,
			"FAKE_CLAUDE_EXIT_CODE=2",
		},
	})

	err := e.Run(ctx, func(_ Event) error { return nil })
	if err == nil {
		t.Fatal("expected error from Run when subprocess exits non-zero, got nil")
	}
	if !strings.Contains(err.Error(), stderrMsg) {
		t.Errorf("expected error to contain stderr message %q, got: %v", stderrMsg, err)
	}
	if !strings.Contains(err.Error(), "code 2") {
		t.Errorf("expected error to mention exit code 2, got: %v", err)
	}
}

func TestExecutorContextCancellation(t *testing.T) {
	binPath := buildFakeClaude(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Prompt:     "test",
	})

	err := e.Run(ctx, func(_ Event) error { return nil })
	if err != nil {
		t.Errorf("expected nil error on context cancellation, got: %v", err)
	}
}

func TestExecutorCleansUpDownloadedImageFiles(t *testing.T) {
	binPath := buildFakeClaude(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")
	origDownloadImage := downloadImage
	downloadImage = func(url, dst string) error {
		return os.WriteFile(dst, []byte("png"), 0o600)
	}
	t.Cleanup(func() {
		downloadImage = origDownloadImage
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e := NewExecutor(Options{
		BinaryPath: binPath,
		CWD:        t.TempDir(),
		Prompt:     "describe the image",
		ImageURLs:  []string{"https://example.com/sample.png"},
		Env:        []string{"FAKE_CLAUDE_ARGS_FILE=" + argsFile},
	})

	if err := e.Run(ctx, func(_ Event) error { return nil }); err != nil {
		t.Fatalf("Run: %v", err)
	}

	argv := readArgsFile(t, argsFile)
	if len(argv) == 0 {
		t.Fatal("empty argv")
	}
	prompt := argv[len(argv)-1]

	var downloadedPath string
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "- ") && strings.Contains(line, "gsd-upload-") {
			downloadedPath = strings.TrimSpace(strings.TrimPrefix(line, "- "))
			break
		}
	}
	if downloadedPath == "" {
		t.Fatalf("expected prompt to include downloaded image path, got %q", prompt)
	}
	if _, err := os.Stat(downloadedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected downloaded image to be cleaned up, stat err=%v", err)
	}
}
