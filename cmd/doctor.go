package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/sockapi"
	"github.com/spf13/cobra"
)

// Diagnose the user's setup. Each check is independent so partial failures
// still surface useful info — don't bail on the first failure.
//
// Exit code is non-zero if any required check fails so scripts can rely on it.

type checkResult struct {
	name    string
	passed  bool
	detail  string
	fixHint string
}

func (r checkResult) String() string {
	mark := "✓"
	if !r.passed {
		mark = "✗"
	}
	line := fmt.Sprintf("  %s %s", mark, r.name)
	if r.detail != "" {
		line += "  " + r.detail
	}
	if !r.passed && r.fixHint != "" {
		line += "\n      → " + r.fixHint
	}
	return line
}

func checkClaude() checkResult {
	path, err := exec.LookPath("claude")
	if err != nil {
		return checkResult{
			name:    "claude binary on PATH",
			passed:  false,
			detail:  "(not found)",
			fixHint: "install Claude Code: curl -fsSL https://claude.ai/install.sh | bash",
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, verr := exec.CommandContext(ctx, path, "--version").Output()
	if verr != nil {
		return checkResult{
			name:    "claude binary on PATH",
			passed:  false,
			detail:  "(found but failed to run)",
			fixHint: "reinstall Claude Code: curl -fsSL https://claude.ai/install.sh | bash",
		}
	}
	return checkResult{
		name:   "claude binary on PATH",
		passed: true,
		detail: fmt.Sprintf("(%s)", strings.TrimSpace(string(out))),
	}
}

func checkClaudeAuth() checkResult {
	// Spawning claude with a trivial prompt is the only honest way to verify
	// auth — `claude --version` works even when logged out. Use a short
	// timeout so we don't block on a hung session if something is wrong.
	path, err := exec.LookPath("claude")
	if err != nil {
		return checkResult{
			name:    "claude logged in",
			passed:  false,
			detail:  "(claude not installed)",
			fixHint: "install claude first, then run: claude",
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--print", "ok")
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil || strings.Contains(strings.ToLower(output), "not logged in") || strings.Contains(strings.ToLower(output), "please run /login") {
		return checkResult{
			name:    "claude logged in",
			passed:  false,
			detail:  "(authentication missing)",
			fixHint: "run: claude    (then complete browser login)",
		}
	}
	return checkResult{
		name:   "claude logged in",
		passed: true,
	}
}

func checkPiExtension() checkResult {
	exe, err := os.Executable()
	if err != nil {
		return checkResult{name: "pi extension installed", passed: false, detail: "(can't resolve daemon path)"}
	}
	extDir := filepath.Join(filepath.Dir(exe), "pi-extension")
	if _, err := os.Stat(filepath.Join(extDir, "index.ts")); err != nil {
		return checkResult{
			name:    "pi extension installed",
			passed:  false,
			detail:  fmt.Sprintf("(missing at %s)", extDir),
			fixHint: "reinstall: curl -fsSL https://install.gsd.build | sh",
		}
	}
	// Verify the platform-specific claude SDK native binary exists. This is
	// the bug class that motivated this command — silent missing-binary
	// failures producing empty assistant messages and $0 cost.
	sdkBaseDir := filepath.Join(extDir, "node_modules", "@anthropic-ai")
	entries, err := os.ReadDir(sdkBaseDir)
	if err != nil {
		return checkResult{
			name:    "pi extension installed",
			passed:  false,
			detail:  "(node_modules missing)",
			fixHint: fmt.Sprintf("cd %s && npm ci --omit=dev --include=optional", extDir),
		}
	}
	hasNativeBinary := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "claude-agent-sdk-") && e.Name() != "claude-agent-sdk" {
			if _, err := os.Stat(filepath.Join(sdkBaseDir, e.Name(), "claude")); err == nil {
				hasNativeBinary = true
				break
			}
		}
	}
	if !hasNativeBinary {
		return checkResult{
			name:    "pi extension installed",
			passed:  false,
			detail:  "(no platform-native claude SDK binary in node_modules)",
			fixHint: fmt.Sprintf("cd %s && npm ci --omit=dev --include=optional", extDir),
		}
	}
	return checkResult{name: "pi extension installed", passed: true}
}

func checkPi() checkResult {
	// Daemon resolves pi from $GSD_PI_BINARY or PATH.
	piPath := os.Getenv("GSD_PI_BINARY")
	if piPath == "" {
		var err error
		piPath, err = exec.LookPath("pi")
		if err != nil {
			return checkResult{
				name:    "pi binary on PATH",
				passed:  false,
				detail:  "(not found)",
				fixHint: "install pi: npm install -g @mariozechner/pi-coding-agent",
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, piPath, "--version").Output()
	if err != nil {
		return checkResult{
			name:    "pi binary on PATH",
			passed:  false,
			detail:  fmt.Sprintf("(at %s but failed to run)", piPath),
			fixHint: "reinstall pi: npm install -g @mariozechner/pi-coding-agent",
		}
	}
	return checkResult{
		name:   "pi binary on PATH",
		passed: true,
		detail: fmt.Sprintf("(v%s)", strings.TrimSpace(string(out))),
	}
}

func checkConfig() checkResult {
	cfg, err := config.Load()
	if err != nil {
		return checkResult{
			name:    "machine paired",
			passed:  false,
			detail:  "(no config)",
			fixHint: "run: gsd-cloud login <CODE>    (get the code from app.gsd.build)",
		}
	}
	if cfg.MachineID == "" || cfg.AuthToken == "" {
		return checkResult{
			name:    "machine paired",
			passed:  false,
			detail:  "(config exists but is incomplete)",
			fixHint: "re-pair: gsd-cloud login <CODE>",
		}
	}
	return checkResult{name: "machine paired", passed: true, detail: fmt.Sprintf("(machine %s)", shortID(cfg.MachineID))}
}

func checkInstallationIdentity() checkResult {
	cfg, err := config.Load()
	if err != nil {
		return checkResult{
			name:    "stable machine identity",
			passed:  false,
			detail:  "(no config)",
			fixHint: "run: gsd-cloud login <CODE>",
		}
	}
	if cfg.InstallationID == "" {
		return checkResult{
			name:    "stable machine identity",
			passed:  false,
			detail:  "(missing installationId)",
			fixHint: "re-pair this daemon: gsd-cloud login <CODE>",
		}
	}
	return checkResult{name: "stable machine identity", passed: true, detail: fmt.Sprintf("(%s)", shortID(cfg.InstallationID))}
}

func checkDaemonRunning() checkResult {
	sockPath, err := socketPath()
	if err != nil {
		return checkResult{name: "daemon running", passed: false, detail: "(socket path unavailable)"}
	}
	if !sockapi.IsDaemonRunning(sockPath) {
		return checkResult{
			name:    "daemon running",
			passed:  false,
			detail:  "(no live daemon)",
			fixHint: "run: gsd-cloud start",
		}
	}
	status, err := sockapi.QueryStatus(sockPath)
	if err != nil {
		return checkResult{name: "daemon running", passed: false, detail: "(socket exists but unresponsive)"}
	}
	if !status.RelayConnected {
		return checkResult{
			name:    "daemon running",
			passed:  false,
			detail:  "(relay disconnected)",
			fixHint: "check network connectivity to " + status.RelayURL,
		}
	}
	return checkResult{name: "daemon running", passed: true, detail: "(relay connected)"}
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose the local installation",
	Long:  "Check that all dependencies and configuration needed to run gsd-cloud are present and working.",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("gsd-cloud doctor")
		fmt.Println()
		checks := []checkResult{
			checkClaude(),
			checkClaudeAuth(),
			checkPi(),
			checkPiExtension(),
			checkConfig(),
			checkInstallationIdentity(),
			checkDaemonRunning(),
		}
		failed := 0
		for _, r := range checks {
			fmt.Println(r)
			if !r.passed {
				failed++
			}
		}
		fmt.Println()
		if failed == 0 {
			fmt.Println("All checks passed.")
			return nil
		}
		fmt.Printf("%d check(s) failed.\n", failed)
		os.Exit(1)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
