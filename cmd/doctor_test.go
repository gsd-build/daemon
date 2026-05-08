package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckPiAcceptsPiRustWithoutVersionFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pi-rust")
	script := `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  --version)
    printf '%s\n' 'Unknown argument: --version' >&2
    exit 1
    ;;
  --help)
    printf '%s\n' 'Usage: pi-rust [-p|--print [PROMPT...] | --mode rpc|trace|print|json]'
    exit 1
    ;;
esac
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi-rust: %v", err)
	}
	t.Setenv("GSD_PI_BINARY", path)

	result := checkPi()
	if !result.passed {
		t.Fatalf("checkPi() failed: %+v", result)
	}
	if !strings.Contains(result.detail, "pi-rust") {
		t.Fatalf("checkPi() detail = %q, want pi-rust", result.detail)
	}
}
