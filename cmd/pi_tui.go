package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gsd-build/daemon/internal/agents"
	"github.com/gsd-build/daemon/internal/agentterminal"
	"github.com/gsd-build/daemon/internal/config"
	"github.com/gsd-build/daemon/internal/pi"
	"github.com/gsd-build/daemon/internal/update"
	protocol "github.com/gsd-build/protocol-go"
	"github.com/spf13/cobra"
)

const defaultPiTUIModels = "claude-opus-4-7,claude-opus-4-6,claude-sonnet-4-6,claude-haiku-4-5-20251001,gpt-5.5,gpt-5.4,z-ai/glm-4.7-flash,deepseek/deepseek-v3.2,moonshotai/kimi-k2.5,qwen/qwen3-coder"

var piTUIFlags struct {
	cwd                    string
	provider               string
	model                  string
	models                 string
	sessionID              string
	sessionFile            string
	noSession              bool
	extensionPath          string
	customInstructions     string
	customInstructionsFile string
	skillPaths             []string
	disableSkills          bool
	disableAgentTools      bool
	browserGrantID         string
	browserID              string
	browserSessionID       string
	taskID                 string
	channelID              string
	projectID              string
	machineID              string
	printCommand           bool
	extraPiArgs            []string
}

var piTUICmd = &cobra.Command{
	Use:   "pi-tui [initial message...]",
	Short: "Start Pi TUI with the daemon extension",
	Long:  "Start Pi TUI with the daemon extension, provider registry, model set, session path, and task-scoped tool environment.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPiTUI(cmd.Context(), args)
	},
}

func init() {
	piTUICmd.Flags().StringVar(&piTUIFlags.cwd, "cwd", ".", "Project working directory")
	piTUICmd.Flags().StringVar(&piTUIFlags.provider, "provider", "claude-cli", "Pi provider id")
	piTUICmd.Flags().StringVar(&piTUIFlags.model, "model", "claude-sonnet-4-6", "Pi model id")
	piTUICmd.Flags().StringVar(&piTUIFlags.models, "models", defaultPiTUIModels, "Comma-separated model cycling list")
	piTUICmd.Flags().StringVar(&piTUIFlags.sessionID, "session-id", "local-tui", "GSD session id used for Pi persistence and tool scope")
	piTUICmd.Flags().StringVar(&piTUIFlags.sessionFile, "session-file", "", "Pi session file path")
	piTUICmd.Flags().BoolVar(&piTUIFlags.noSession, "no-session", false, "Run Pi without a session file")
	piTUICmd.Flags().StringVar(&piTUIFlags.extensionPath, "extension", "", "Pi extension path")
	piTUICmd.Flags().StringVar(&piTUIFlags.customInstructions, "custom-instructions", os.Getenv("GSD_CUSTOM_INSTRUCTIONS"), "Account-level custom instructions")
	piTUICmd.Flags().StringVar(&piTUIFlags.customInstructionsFile, "custom-instructions-file", "", "File containing account-level custom instructions")
	piTUICmd.Flags().StringArrayVar(&piTUIFlags.skillPaths, "skill", nil, "Skill file or directory passed to Pi")
	piTUICmd.Flags().BoolVar(&piTUIFlags.disableSkills, "no-skills", false, "Disable skill discovery and explicit skills")
	piTUICmd.Flags().BoolVar(&piTUIFlags.disableAgentTools, "no-agent-tools", false, "Disable local agent-terminal tool backend")
	piTUICmd.Flags().StringVar(&piTUIFlags.browserGrantID, "browser-grant-id", os.Getenv("GSD_BROWSER_GRANT_ID"), "Task browser grant id")
	piTUICmd.Flags().StringVar(&piTUIFlags.browserID, "browser-id", os.Getenv("GSD_BROWSER_ID"), "Task browser id")
	piTUICmd.Flags().StringVar(&piTUIFlags.browserSessionID, "browser-session-id", os.Getenv("GSD_BROWSER_SESSION_ID"), "Task browser session id")
	piTUICmd.Flags().StringVar(&piTUIFlags.taskID, "task-id", "local-tui-task", "Task id used for tool scope")
	piTUICmd.Flags().StringVar(&piTUIFlags.channelID, "channel-id", "local-tui-channel", "Channel id used for tool scope")
	piTUICmd.Flags().StringVar(&piTUIFlags.projectID, "project-id", "local-tui-project", "Project id used for tool scope")
	piTUICmd.Flags().StringVar(&piTUIFlags.machineID, "machine-id", "", "Machine id used for tool scope")
	piTUICmd.Flags().BoolVar(&piTUIFlags.printCommand, "print-command", false, "Print the Pi command and exit")
	piTUICmd.Flags().StringArrayVar(&piTUIFlags.extraPiArgs, "pi-arg", nil, "Additional raw Pi flag")
	rootCmd.AddCommand(piTUICmd)
}

func runPiTUI(parent context.Context, initialMessages []string) error {
	cwd, err := filepath.Abs(piTUIFlags.cwd)
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	extensionPath, err := resolvePiTUIExtensionPath(piTUIFlags.extensionPath)
	if err != nil {
		return err
	}
	if err := update.EnsureExtensionHealthy(filepath.Dir(extensionPath)); err != nil {
		return fmt.Errorf("prepare pi extension: %w", err)
	}
	customInstructions, err := piTUICustomInstructions()
	if err != nil {
		return err
	}
	homeDir, err := piTUIHomeDir()
	if err != nil {
		return err
	}
	sessionFile, err := piTUISessionFile(homeDir)
	if err != nil {
		return err
	}
	agentDir := filepath.Join(homeDir, ".gsd-cloud", "agents")
	subagentsPrompt := piTUISubagentsPrompt(agentDir)
	taskID := strings.TrimSpace(piTUIFlags.taskID)
	if taskID == "" {
		taskID = "local-tui-task"
	}
	channelID := strings.TrimSpace(piTUIFlags.channelID)
	if channelID == "" {
		channelID = "local-tui-channel"
	}
	machineID := strings.TrimSpace(piTUIFlags.machineID)
	if machineID == "" {
		machineID = piTUIMachineID()
	}

	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	piOpts := pi.Options{
		BinaryPath:         piTUIBinaryPath(),
		CWD:                cwd,
		Model:              strings.TrimSpace(piTUIFlags.model),
		ResumeSession:      sessionFile,
		TaskID:             taskID,
		SessionID:          strings.TrimSpace(piTUIFlags.sessionID),
		ChannelID:          channelID,
		CustomInstructions: customInstructions,
		ExtensionPath:      extensionPath,
		Provider:           strings.TrimSpace(piTUIFlags.provider),
		SkillPaths:         piTUIFlags.skillPaths,
		DisableSkills:      piTUIFlags.disableSkills,
		BrowserGrantID:     strings.TrimSpace(piTUIFlags.browserGrantID),
		BrowserID:          strings.TrimSpace(piTUIFlags.browserID),
		BrowserSessionID:   strings.TrimSpace(piTUIFlags.browserSessionID),
		WarmClaudeSDK:      piTUIWarmClaudeSDK(),
		PlanCapability:     piTUIPlanCapabilityFromEnv(),
		DaemonSocketPath:   filepath.Join(homeDir, ".gsd-cloud", "daemon.sock"),
		ParentSessionID:    strings.TrimSpace(piTUIFlags.sessionID),
		AgentDir:           agentDir,
		SubagentsPrompt:    subagentsPrompt,
	}
	if piOpts.SessionID == "" {
		piOpts.SessionID = "local-tui"
		piOpts.ParentSessionID = piOpts.SessionID
	}

	var stopAgentTools func()
	if !piTUIFlags.disableAgentTools {
		control, stop, err := startPiTUIAgentTools(ctx, agentterminal.TaskScope{
			SessionID:  piOpts.SessionID,
			ChannelID:  channelID,
			TaskID:     taskID,
			ProjectID:  strings.TrimSpace(piTUIFlags.projectID),
			MachineID:  machineID,
			ProjectCWD: cwd,
		})
		if err != nil {
			return fmt.Errorf("start agent tool control: %w", err)
		}
		stopAgentTools = stop
		defer stopAgentTools()
		piOpts.AgentToolsSocket = control.SocketPath
		piOpts.AgentToolsToken = control.Token
	}

	args := pi.ProcessArgs(piOpts)
	if models := strings.TrimSpace(piTUIFlags.models); models != "" {
		args = append(args, "--models", models)
	}
	if sessionFile != "" {
		args = append(args, "--session", sessionFile)
	} else {
		args = append(args, "--no-session")
	}
	args = append(args, piTUIFlags.extraPiArgs...)
	args = append(args, initialMessages...)

	if piTUIFlags.printCommand {
		fmt.Println(shellJoin(append([]string{piOpts.BinaryPath}, args...)))
		return nil
	}

	piCmd := exec.CommandContext(ctx, piOpts.BinaryPath, args...)
	piCmd.Dir = cwd
	piCmd.Env = pi.ProcessEnv(ctx, os.Environ(), piOpts)
	piCmd.Stdin = os.Stdin
	piCmd.Stdout = os.Stdout
	piCmd.Stderr = os.Stderr
	if runtime.GOOS != "windows" {
		piCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := piCmd.Start(); err != nil {
		return fmt.Errorf("start pi tui: %w", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- piCmd.Wait() }()
	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		terminatePiTUIProcess(piCmd)
		return <-waitCh
	}
}

func resolvePiTUIExtensionPath(flagValue string) (string, error) {
	candidates := []string{}
	if flagValue != "" {
		candidates = append(candidates, flagValue)
	}
	if envValue := os.Getenv("GSD_PI_EXTENSION_PATH"); envValue != "" {
		candidates = append(candidates, envValue)
	}
	if repoRoot, err := piTUIRepoRoot(); err == nil {
		candidates = append(candidates, filepath.Join(repoRoot, "internal", "pi", "extension", "index.ts"))
	}
	if home, err := piTUIHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".gsd-cloud", "bin", "pi-extension", "index.ts"))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return "", fmt.Errorf("resolve extension path: %w", err)
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("pi extension not found; pass --extension or set GSD_PI_EXTENSION_PATH")
}

func piTUIRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "internal", "pi", "extension", "index.ts")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repo root not found")
		}
		dir = parent
	}
}

func piTUICustomInstructions() (string, error) {
	sections := []string{}
	if text := strings.TrimSpace(piTUIFlags.customInstructions); text != "" {
		sections = append(sections, text)
	}
	if path := strings.TrimSpace(piTUIFlags.customInstructionsFile); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read custom instructions file: %w", err)
		}
		if text := strings.TrimSpace(string(data)); text != "" {
			sections = append(sections, text)
		}
	}
	return strings.Join(sections, "\n\n"), nil
}

func piTUISessionFile(homeDir string) (string, error) {
	if piTUIFlags.noSession {
		return "", nil
	}
	if path := strings.TrimSpace(piTUIFlags.sessionFile); path != "" {
		return filepath.Abs(path)
	}
	sessionID := strings.TrimSpace(piTUIFlags.sessionID)
	if sessionID == "" {
		sessionID = "local-tui"
	}
	dir := filepath.Join(homeDir, ".gsd-cloud", "pi-sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create pi session directory: %w", err)
	}
	return filepath.Join(dir, safePiTUISessionFilename(sessionID)+".jsonl"), nil
}

func safePiTUISessionFilename(sessionID string) string {
	var b strings.Builder
	b.Grow(len(sessionID))
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func piTUIHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("home directory is empty")
	}
	return home, nil
}

func piTUIBinaryPath() string {
	if value := strings.TrimSpace(os.Getenv("GSD_PI_BINARY")); value != "" {
		return value
	}
	return "pi"
}

func piTUIWarmClaudeSDK() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GSD_WARM_CLAUDE_SDK"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func piTUIPlanCapabilityFromEnv() *protocol.PlanCapability {
	token := strings.TrimSpace(os.Getenv("GSD_PLAN_CAPABILITY_TOKEN"))
	apiBaseURL := strings.TrimSpace(os.Getenv("GSD_PLAN_API_BASE_URL"))
	expiresAt := strings.TrimSpace(os.Getenv("GSD_PLAN_CAPABILITY_EXPIRES_AT"))
	if token == "" || apiBaseURL == "" || expiresAt == "" {
		return nil
	}
	return &protocol.PlanCapability{
		ID:         strings.TrimSpace(os.Getenv("GSD_PLAN_CAPABILITY_ID")),
		AttemptID:  strings.TrimSpace(os.Getenv("GSD_PLAN_CAPABILITY_ATTEMPT_ID")),
		Token:      token,
		APIBaseURL: apiBaseURL,
		ExpiresAt:  expiresAt,
	}
}

func piTUIMachineID() string {
	cfg, err := config.Load()
	if err == nil && strings.TrimSpace(cfg.MachineID) != "" {
		return strings.TrimSpace(cfg.MachineID)
	}
	return "local-tui-machine"
}

func piTUISubagentsPrompt(agentDir string) string {
	defs := readPiTUIAgents(agentDir)
	return agents.BuildPrompt(defs)
}

func readPiTUIAgents(agentDir string) []agents.Definition {
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		return nil
	}
	defs := make([]agents.Definition, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(agentDir, entry.Name()))
		if err != nil {
			continue
		}
		var def agents.Definition
		if err := json.Unmarshal(data, &def); err != nil {
			continue
		}
		if strings.TrimSpace(def.Name) != "" {
			defs = append(defs, def)
		}
	}
	return defs
}

func startPiTUIAgentTools(ctx context.Context, scope agentterminal.TaskScope) (agentterminal.TaskControl, func(), error) {
	manager := agentterminal.NewManager(noopAgentTerminalSender{}, agentterminal.DefaultLimits())
	controlServer := agentterminal.NewControlServer(manager, "")
	control, err := controlServer.StartTask(ctx, scope)
	if err != nil {
		return agentterminal.TaskControl{}, nil, err
	}
	stop := func() {
		controlServer.StopTask(scope.TaskID)
	}
	return control, stop, nil
}

func terminatePiTUIProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if runtime.GOOS != "windows" {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		go func(pid int) {
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			<-timer.C
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}(cmd.Process.Pid)
		return
	}
	_ = cmd.Process.Kill()
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			quoted = append(quoted, "''")
			continue
		}
		if strings.IndexFunc(arg, func(r rune) bool {
			return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./:=,", r))
		}) == -1 {
			quoted = append(quoted, arg)
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\\''")+"'")
	}
	return strings.Join(quoted, " ")
}

type noopAgentTerminalSender struct{}

func (noopAgentTerminalSender) SendAgentTerminalStarted(agentterminal.Job) error { return nil }
func (noopAgentTerminalSender) SendAgentTerminalUpdated(agentterminal.Job) error { return nil }
func (noopAgentTerminalSender) SendTerminalOutput(string, string, string, int64, []byte) error {
	return nil
}
func (noopAgentTerminalSender) SendTerminalSnapshot(string, string, string, int64, []byte) error {
	return nil
}
func (noopAgentTerminalSender) SendTerminalExit(string, string, string, string, int, string, time.Time) error {
	return nil
}
func (noopAgentTerminalSender) SendTerminalError(string, string, string, string, string) error {
	return nil
}
func (noopAgentTerminalSender) SendLocalServerDetected(string, string, string, string, string, int, string, string, string) error {
	return nil
}
