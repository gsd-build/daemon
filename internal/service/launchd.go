package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launchdLabel = "build.gsd.cloud.daemon"

// launchdPlatform manages the daemon via macOS launchd.
type launchdPlatform struct{}

func (l *launchdPlatform) plistPath() string {
	return filepath.Join(HomeDir(), "Library", "LaunchAgents", launchdLabel+".plist")
}

func (l *launchdPlatform) generatePlist() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>start</string>
		<string>--service</string>
	</array>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
	<key>ThrottleInterval</key>
	<integer>10</integer>
</dict>
</plist>
`, launchdLabel, BinaryPath(), LogPath(), LogPath())
}

func (l *launchdPlatform) Install() error {
	launchAgentsDir := filepath.Dir(l.plistPath())
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	logsDir := filepath.Dir(LogPath())
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}

	if err := os.WriteFile(l.plistPath(), []byte(l.generatePlist()), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	out, err := exec.Command("launchctl", "load", l.plistPath()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl load: %w: %s", err, out)
	}
	return nil
}

func (l *launchdPlatform) Uninstall() error {
	if !l.IsInstalled() {
		return fmt.Errorf("service is not installed")
	}

	out, err := exec.Command("launchctl", "unload", l.plistPath()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl unload: %w: %s", err, out)
	}

	if err := os.Remove(l.plistPath()); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func (l *launchdPlatform) Start() error {
	out, err := exec.Command("launchctl", "start", launchdLabel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl start: %w: %s", err, out)
	}
	return nil
}

func (l *launchdPlatform) Stop() error {
	out, err := exec.Command("launchctl", "stop", launchdLabel).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl stop: %w: %s", err, out)
	}
	return nil
}

func (l *launchdPlatform) IsInstalled() bool {
	_, err := os.Stat(l.plistPath())
	return err == nil
}

func (l *launchdPlatform) IsRunning() bool {
	out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}
