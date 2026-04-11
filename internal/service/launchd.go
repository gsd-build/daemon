package service

// launchdPlatform manages the daemon via macOS launchd.
type launchdPlatform struct{}

func (l *launchdPlatform) Install() error   { return nil }
func (l *launchdPlatform) Uninstall() error { return nil }
func (l *launchdPlatform) Start() error     { return nil }
func (l *launchdPlatform) Stop() error      { return nil }
func (l *launchdPlatform) IsInstalled() bool { return false }
func (l *launchdPlatform) IsRunning() bool   { return false }
