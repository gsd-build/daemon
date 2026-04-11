package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const systemdServiceName = "gsd-cloud"

// systemdPlatform manages the daemon via Linux systemd.
type systemdPlatform struct{}

func (s *systemdPlatform) unitPath() string {
	return filepath.Join(HomeDir(), ".config", "systemd", "user", systemdServiceName+".service")
}

func (s *systemdPlatform) generateUnit() string {
	return `[Unit]
Description=GSD Cloud Daemon
After=network-online.target

[Service]
Type=simple
ExecStart=%h/.gsd-cloud/bin/gsd-cloud start --service
Restart=always
RestartSec=5
StartLimitBurst=5
StartLimitIntervalSec=300

[Install]
WantedBy=default.target
`
}

func (s *systemdPlatform) Install() error {
	unitDir := filepath.Dir(s.unitPath())
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}

	logsDir := filepath.Dir(LogPath())
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}

	if err := os.WriteFile(s.unitPath(), []byte(s.generateUnit()), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, out)
	}

	out, err = exec.Command("systemctl", "--user", "enable", "--now", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl enable --now: %w: %s", err, out)
	}
	return nil
}

func (s *systemdPlatform) Uninstall() error {
	if !s.IsInstalled() {
		return fmt.Errorf("service is not installed")
	}

	out, err := exec.Command("systemctl", "--user", "stop", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl stop: %w: %s", err, out)
	}

	out, err = exec.Command("systemctl", "--user", "disable", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl disable: %w: %s", err, out)
	}

	if err := os.Remove(s.unitPath()); err != nil {
		return fmt.Errorf("remove unit file: %w", err)
	}

	out, err = exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, out)
	}
	return nil
}

func (s *systemdPlatform) Start() error {
	out, err := exec.Command("systemctl", "--user", "start", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl start: %w: %s", err, out)
	}
	return nil
}

func (s *systemdPlatform) Stop() error {
	out, err := exec.Command("systemctl", "--user", "stop", systemdServiceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl stop: %w: %s", err, out)
	}
	return nil
}

func (s *systemdPlatform) IsInstalled() bool {
	_, err := os.Stat(s.unitPath())
	return err == nil
}

func (s *systemdPlatform) IsRunning() bool {
	out, err := exec.Command("systemctl", "--user", "is-active", systemdServiceName).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}
