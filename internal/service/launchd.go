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

func (l *launchdPlatform) domainTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func (l *launchdPlatform) serviceTarget() string {
	return l.domainTarget() + "/" + launchdLabel
}

func (l *launchdPlatform) run(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

func (l *launchdPlatform) isLoaded() bool {
	out, err := exec.Command("launchctl", "print", l.serviceTarget()).CombinedOutput()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

func (l *launchdPlatform) generatePlist() string {
	// Capture the user's current PATH so the daemon can find `claude` and
	// other tools. launchd provides only a minimal PATH by default.
	userPath := os.Getenv("PATH")
	if userPath == "" {
		userPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
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
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>%s</string>
	</dict>
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
`, launchdLabel, BinaryPath(), userPath, LogPath(), LogPath())
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

	return l.run("bootstrap", l.domainTarget(), l.plistPath())
}

func (l *launchdPlatform) Uninstall() error {
	if !l.IsInstalled() {
		return fmt.Errorf("service is not installed")
	}

	if l.isLoaded() {
		if err := l.run("bootout", l.serviceTarget()); err != nil {
			return err
		}
	}

	if err := os.Remove(l.plistPath()); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func (l *launchdPlatform) Start() error {
	if l.isLoaded() {
		return l.run("kickstart", "-k", l.serviceTarget())
	}
	return l.run("bootstrap", l.domainTarget(), l.plistPath())
}

func (l *launchdPlatform) Stop() error {
	if !l.isLoaded() {
		return nil
	}
	return l.run("bootout", l.serviceTarget())
}

func (l *launchdPlatform) IsInstalled() bool {
	_, err := os.Stat(l.plistPath())
	return err == nil
}

func (l *launchdPlatform) IsRunning() bool {
	out, err := exec.Command("launchctl", "print", l.serviceTarget()).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "state = running")
}
