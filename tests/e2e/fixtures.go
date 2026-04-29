package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gsd-build/daemon/internal/config"
)

func writeFakePi(t *testing.T, destDir string) string {
	t.Helper()
	destPath := filepath.Join(destDir, "fake-pi")
	script := `#!/bin/sh
IFS= read -r _prompt || true
printf '%s\n' '{"type":"agent_start"}'
if [ "$FAKE_PI_ASK_HUMAN" = "1" ]; then
  printf '%s\n' '{"type":"extension_ui_request","id":"request_1","method":"input","title":"Which path should I take?","placeholder":"Type your answer..."}'
  IFS= read -r response_frame || true
  if [ -n "$FAKE_PI_RESPONSE_FILE" ]; then
    printf '%s\n' "$response_frame" > "$FAKE_PI_RESPONSE_FILE"
  fi
fi
printf '%s\n' '{"type":"message_start","message":{"role":"assistant","content":[],"api":"anthropic-messages","provider":"claude-cli","model":"claude-sonnet-4-6","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"total":0}}}}'
printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"ok"},"message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"api":"anthropic-messages","provider":"claude-cli","model":"claude-sonnet-4-6","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"total":0}}}}'
printf '%s\n' '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":2,"output":3,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.001}}}]}'
`
	if err := os.WriteFile(destPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	return destPath
}

func writeWarmFakePi(t *testing.T, destDir string) string {
	t.Helper()
	path := filepath.Join(destDir, "fake-pi-warm")
	body := `#!/bin/sh
count=0
while IFS= read -r line; do
  case "$line" in
    *'"type":"prompt"'*)
      count=$((count + 1))
      printf '%s\n' '{"type":"response","command":"prompt","success":true}'
      printf '%s\n' "{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"warm-$count\"},\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"warm-$count\"}],\"api\":\"anthropic-messages\",\"provider\":\"claude-cli\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":0,\"cost\":{\"total\":0}}}}"
      printf '%s\n' "{\"type\":\"agent_end\",\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"warm-$count\"}],\"usage\":{\"input\":2,\"output\":3,\"cacheRead\":0,\"cacheWrite\":0,\"cost\":{\"total\":0.001}}}]}"
      ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatalf("write warm fake pi: %v", err)
	}
	return path
}

func writeFakePiExtension(t *testing.T, destDir string) string {
	t.Helper()
	path := filepath.Join(destDir, "pi-extension", "index.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir fake pi extension: %v", err)
	}
	if err := os.WriteFile(path, []byte("export default {};"), 0o600); err != nil {
		t.Fatalf("write fake pi extension: %v", err)
	}
	return path
}

// makeTestHome creates a temp directory to use as the daemon's home and
// returns the absolute path. The directory is removed at test end.
func makeTestHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gsd-daemon-e2e-*")
	if err != nil {
		t.Fatalf("makeTestHome: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// makeTestConfig returns a daemon Config pointing at the given relay URL.
func makeTestConfig(relayURL, machineID, authToken string) *config.Config {
	return &config.Config{
		MachineID: machineID,
		AuthToken: authToken,
		ServerURL: "",
		RelayURL:  relayURL,
	}
}
