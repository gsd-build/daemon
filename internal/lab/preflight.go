package lab

import (
	"os"
	"os/exec"
)

type PreflightOptions struct {
	CWD           string
	Provider      string
	Model         string
	FakeMode      bool
	PiBinary      string
	ClaudeBinary  string
	ExtensionPath string
}

type PreflightCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type PreflightResult struct {
	OK     bool             `json:"ok"`
	Checks []PreflightCheck `json:"checks"`
}

func RunPreflight(opts PreflightOptions) PreflightResult {
	result := PreflightResult{OK: true}
	add := func(name string, ok bool, message string) {
		result.Checks = append(result.Checks, PreflightCheck{Name: name, OK: ok, Message: message})
		if !ok {
			result.OK = false
		}
	}
	info, err := os.Stat(opts.CWD)
	add("cwd", err == nil && info != nil && info.IsDir(), "cwd must be an existing directory")
	add("provider", opts.Provider != "", "provider is required")
	add("model", opts.Model != "", "model is required")
	if !opts.FakeMode {
		pi := opts.PiBinary
		if pi == "" {
			pi = "pi"
		}
		_, err = exec.LookPath(pi)
		add("pi", err == nil, "pi binary must be on PATH or configured")
		if opts.Provider == "claude-cli" {
			claude := opts.ClaudeBinary
			if claude == "" {
				claude = "claude"
			}
			_, err = exec.LookPath(claude)
			add("claude", err == nil, "claude binary must be on PATH for claude-cli")
		}
	}
	if opts.ExtensionPath != "" {
		_, err = os.Stat(opts.ExtensionPath)
		add("extension", err == nil, "extension path must exist")
	}
	return result
}
