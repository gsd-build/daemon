package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type RunnerOptions struct {
	BinaryPath    string
	SessionDir    string
	RelayURL      string
	MachineID     string
	AuthToken     string
	FakeMode      bool
	PiBinary      string
	ExtensionPath string
}

type Runner struct {
	opts RunnerOptions
	cmd  *exec.Cmd
}

func NewRunner(opts RunnerOptions) *Runner {
	return &Runner{opts: opts}
}

func (r *Runner) HomeDir() string {
	return filepath.Join(r.opts.SessionDir, "daemon-home")
}

func (r *Runner) WriteConfig() error {
	home := r.HomeDir()
	configDir := filepath.Join(home, ".gsd-cloud")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	cfg := map[string]any{
		"machineId":          r.opts.MachineID,
		"authToken":          r.opts.AuthToken,
		"serverUrl":          "http://127.0.0.1",
		"relayUrl":           r.opts.RelayURL,
		"taskTimeoutMinutes": 30,
		"logLevel":           "debug",
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, "config.json"), data, 0o600)
}

func (r *Runner) Start(ctx context.Context) error {
	if r.opts.BinaryPath == "" {
		return fmt.Errorf("daemon binary path is required")
	}
	if err := r.WriteConfig(); err != nil {
		return err
	}
	if r.opts.FakeMode && r.opts.PiBinary == "" {
		piPath, err := WriteFakePi(filepath.Join(r.opts.SessionDir, "fake"))
		if err != nil {
			return err
		}
		r.opts.PiBinary = piPath
	}
	r.cmd = exec.CommandContext(ctx, r.opts.BinaryPath, "start", "--foreground")
	r.cmd.Env = append(os.Environ(),
		"HOME="+r.HomeDir(),
		"GSD_PI_BINARY="+r.opts.PiBinary,
		"GSD_PI_EXTENSION_PATH="+r.opts.ExtensionPath,
	)
	r.cmd.Stdout = os.Stdout
	r.cmd.Stderr = os.Stderr
	return r.cmd.Start()
}

func (r *Runner) Wait() error {
	if r.cmd == nil {
		return nil
	}
	return r.cmd.Wait()
}
