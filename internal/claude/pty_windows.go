//go:build windows

// Package claude does not support Windows. The executor relies on a
// POSIX pseudo-terminal to work around Node.js's block-buffered pipe
// stdout behavior in the claude CLI, and there is no equivalent in the
// current implementation. Supported platforms are Linux and macOS.
//
// This file is the only file in the package that compiles on Windows.
// It contains a deliberate compile error so builds fail loudly and point
// at the real reason, rather than producing a confusing "undefined
// identifier" failure elsewhere in the tree.
package claude

// #error gsd-build/daemon supports Linux and macOS only; Windows is not a supported target.
var _ struct{ windowsNotSupported_gsd_build_daemon_supports_linux_and_macos_only struct{} } = 1
