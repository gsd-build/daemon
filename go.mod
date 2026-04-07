module github.com/gsd-cloud/daemon

go 1.25.0

require github.com/gsd-cloud/protocol-go v0.0.0

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)

replace github.com/gsd-cloud/protocol-go => ../../packages/protocol-go
