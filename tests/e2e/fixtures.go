package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gsd-build/daemon/internal/config"
)

// buildFakeClaude builds the fake-claude binary from the daemon's
// cmd/fake-claude package into the given directory. Returns the absolute
// path to the built binary.
func buildFakeClaude(t *testing.T, destDir string) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("buildFakeClaude: cannot determine caller location")
	}
	repoRoot, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	if err != nil {
		t.Fatalf("buildFakeClaude: abs path: %v", err)
	}

	binName := "fake-claude"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	destPath := filepath.Join(destDir, binName)

	cmd := exec.Command("go", "build", "-o", destPath, "./cmd/fake-claude")
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("buildFakeClaude: go build failed: %v\n%s", err, output)
	}

	return destPath
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
// The fake-claude binary path is not part of config.Config; tests pass it
// directly to the session manager via session.Options.BinaryPath.
func makeTestConfig(relayURL, machineID, authToken string) *config.Config {
	return &config.Config{
		MachineID: machineID,
		AuthToken: authToken,
		ServerURL: "",
		RelayURL:  relayURL,
	}
}
