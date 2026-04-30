module github.com/gsd-build/daemon

go 1.26.2

require (
	github.com/coder/websocket v1.8.14
	github.com/creack/pty v1.1.24
	github.com/gsd-build/protocol-go v0.29.1
	github.com/spf13/cobra v1.10.2
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)

replace github.com/gsd-build/protocol-go => /Users/lexchristopherson/Developer/gsd/gsd-build-protocol-go/.worktrees/turn-lifecycle-protocol
