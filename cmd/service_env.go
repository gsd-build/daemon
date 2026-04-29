package cmd

import (
	"fmt"
	"strings"

	"github.com/gsd-build/daemon/internal/service"
)

func syncServiceProviderEnvironment(platform service.Platform) error {
	synced, err := platform.SyncEnvironment(service.ProviderEnvironmentKeys)
	if err != nil {
		return err
	}
	if len(synced) > 0 {
		fmt.Printf("Synced service environment: %s\n", strings.Join(synced, ", "))
	}
	return nil
}
