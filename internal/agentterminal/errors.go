//go:build !windows

package agentterminal

import "errors"

var (
	ErrJobNotFound      = errors.New("job not found")
	ErrTerminalNotFound = errors.New("terminal not found")
	ErrTerminalClosed   = errors.New("terminal is closed")
)
