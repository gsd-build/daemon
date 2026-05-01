package browser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeRuntimeInstalled(t *testing.T) {
	bin := writeFakeBrowser(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "gsd-browser 0.1.20"; exit 0; fi
if [ "$1" = "cloud-methods" ]; then echo '{"manifestVersion":1}'; exit 0; fi
if [ "$1" = "daemon" ] && [ "$2" = "health" ]; then echo '{"ok":true,"chromeAvailable":true}'; exit 0; fi
exit 1
`)
	got := ProbeRuntime(context.Background(), bin)
	if !got.Ready || !got.Installed || got.Version != "0.1.20" || !got.MinVersionOK || got.CloudMethodsVersion != 1 || !got.ChromeAvailable {
		t.Fatalf("ProbeRuntime = %+v", got)
	}
}

func TestProbeRuntimeInstalledWithSessionHealth(t *testing.T) {
	bin := writeFakeBrowser(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "gsd-browser 0.1.20"; exit 0; fi
if [ "$1" = "cloud-methods" ]; then echo '{"manifestVersion":1}'; exit 0; fi
if [ "$1" = "daemon" ] && [ "$2" = "health" ]; then echo '{"session":{"browserConnected":true,"status":"healthy"}}'; exit 0; fi
exit 1
`)
	got := ProbeRuntime(context.Background(), bin)
	if !got.Ready || !got.Installed || !got.ChromeAvailable {
		t.Fatalf("ProbeRuntime = %+v", got)
	}
}

func TestProbeRuntimeMissingBinary(t *testing.T) {
	got := ProbeRuntime(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if got.ErrorCode != "browser_not_installed" || got.Ready {
		t.Fatalf("ProbeRuntime = %+v", got)
	}
}

func TestProbeRuntimeOldVersion(t *testing.T) {
	bin := writeFakeBrowser(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "gsd-browser 0.1.18"; exit 0; fi
exit 1
`)
	got := ProbeRuntime(context.Background(), bin)
	if got.ErrorCode != "version_too_old" || got.MinVersionOK || got.Ready {
		t.Fatalf("ProbeRuntime = %+v", got)
	}
}

func TestProbeRuntimeMalformedManifest(t *testing.T) {
	bin := writeFakeBrowser(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "gsd-browser 0.1.20"; exit 0; fi
if [ "$1" = "cloud-methods" ]; then echo '{'; exit 0; fi
exit 1
`)
	got := ProbeRuntime(context.Background(), bin)
	if got.ErrorCode != "manifest_invalid" || got.Ready {
		t.Fatalf("ProbeRuntime = %+v", got)
	}
}

func TestProbeRuntimeChromeMissing(t *testing.T) {
	bin := writeFakeBrowser(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "gsd-browser 0.1.20"; exit 0; fi
if [ "$1" = "cloud-methods" ]; then echo '{"manifestVersion":1}'; exit 0; fi
if [ "$1" = "daemon" ] && [ "$2" = "health" ]; then echo '{"ok":false,"chromeAvailable":false,"error":"no chrome"}'; exit 0; fi
exit 1
`)
	got := ProbeRuntime(context.Background(), bin)
	if got.ErrorCode != "chrome_missing" || got.Ready {
		t.Fatalf("ProbeRuntime = %+v", got)
	}
}

func TestProbeRuntimeSessionHealthChromeMissing(t *testing.T) {
	bin := writeFakeBrowser(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "gsd-browser 0.1.20"; exit 0; fi
if [ "$1" = "cloud-methods" ]; then echo '{"manifestVersion":1}'; exit 0; fi
if [ "$1" = "daemon" ] && [ "$2" = "health" ]; then echo '{"session":{"browserConnected":false,"status":"stopped","reason":"daemon stopped"}}'; exit 0; fi
exit 1
`)
	got := ProbeRuntime(context.Background(), bin)
	if got.ErrorCode != "chrome_missing" || got.ErrorMessage != "daemon stopped" || got.Ready {
		t.Fatalf("ProbeRuntime = %+v", got)
	}
}

func TestProbeRuntimeHonorsEnvPath(t *testing.T) {
	bin := writeFakeBrowser(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "gsd-browser 0.1.20"; exit 0; fi
if [ "$1" = "cloud-methods" ]; then echo '{"manifestVersion":1}'; exit 0; fi
if [ "$1" = "daemon" ] && [ "$2" = "health" ]; then echo '{"ok":true,"chromeAvailable":true}'; exit 0; fi
exit 1
`)
	t.Setenv("GSD_BROWSER_PATH", bin)
	got := ProbeRuntime(context.Background(), "")
	if got.Path != bin || !got.Ready {
		t.Fatalf("ProbeRuntime = %+v", got)
	}
}

func writeFakeBrowser(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gsd-browser")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake browser: %v", err)
	}
	return path
}
