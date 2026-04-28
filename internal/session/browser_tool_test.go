package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakePiEnvRecorder(t *testing.T, envFile string) string {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "fake-pi")
	script := `#!/bin/sh
{
  printf 'GSD_BROWSER_GRANT_ID=%s\n' "$GSD_BROWSER_GRANT_ID"
  printf 'GSD_BROWSER_ID=%s\n' "$GSD_BROWSER_ID"
  printf 'GSD_BROWSER_SESSION_ID=%s\n' "$GSD_BROWSER_SESSION_ID"
} > "$FAKE_PI_ENV_FILE"
IFS= read -r prompt_frame || true
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("FAKE_PI_ENV_FILE", envFile)
	return path
}

func TestActorPiExecutorPassesBrowserGrantToExtension(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "pi.env")
	actor, err := NewActor(testPiOptions(t, Options{
		SessionID:      "session_1",
		Relay:          newFakeRelay(),
		PiBinaryPath:   writeFakePiEnvRecorder(t, envFile),
		BrowserGrantID: "grant_1",
		BrowserID:      "browser_1",
	}))
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}

	err = actor.runPiExecutor(context.Background(), context.Background(), &taskContext{
		TaskID:         "task_1",
		ChannelID:      "channel_1",
		Engine:         "pi",
		BrowserGrantID: "grant_1",
		BrowserID:      "browser_1",
	}, "use the browser")
	if err != nil {
		t.Fatalf("runPiExecutor: %v", err)
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	rendered := string(data)
	for _, want := range []string{
		"GSD_BROWSER_GRANT_ID=grant_1",
		"GSD_BROWSER_ID=browser_1",
		"GSD_BROWSER_SESSION_ID=session_1",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("env missing %q in:\n%s", want, rendered)
		}
	}
}
