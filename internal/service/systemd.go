package service

// systemdPlatform manages the daemon via Linux systemd.
type systemdPlatform struct{}

func (s *systemdPlatform) Install() error   { return nil }
func (s *systemdPlatform) Uninstall() error { return nil }
func (s *systemdPlatform) Start() error     { return nil }
func (s *systemdPlatform) Stop() error      { return nil }
func (s *systemdPlatform) IsInstalled() bool { return false }
func (s *systemdPlatform) IsRunning() bool   { return false }
