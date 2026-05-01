package browser

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const RequiredRuntimeVersion = "0.1.20"
const runtimeProbeStepTimeout = 10 * time.Second

type RuntimeStatus struct {
	Installed           bool
	Version             string
	MinVersion          string
	MinVersionOK        bool
	Path                string
	ErrorCode           string
	ErrorMessage        string
	CloudMethodsVersion int
	ChromeAvailable     bool
	Ready               bool
}

type methodManifest struct {
	ManifestVersion int `json:"manifestVersion"`
}

type daemonHealth struct {
	OK              bool   `json:"ok"`
	ChromeAvailable bool   `json:"chromeAvailable"`
	Error           string `json:"error"`
	Session         struct {
		BrowserConnected bool   `json:"browserConnected"`
		Status           string `json:"status"`
		Reason           string `json:"reason"`
	} `json:"session"`
}

func ProbeRuntime(ctx context.Context, binaryPath string) RuntimeStatus {
	if binaryPath == "" {
		binaryPath = os.Getenv("GSD_BROWSER_PATH")
	}
	if binaryPath == "" {
		binaryPath = "gsd-browser"
	}
	status := RuntimeStatus{MinVersion: RequiredRuntimeVersion, Path: binaryPath}
	versionCtx, cancel := context.WithTimeout(ctx, runtimeProbeStepTimeout)
	defer cancel()

	out, err := exec.CommandContext(versionCtx, binaryPath, "--version").CombinedOutput()
	if err != nil {
		status.ErrorCode = "browser_not_installed"
		status.ErrorMessage = strings.TrimSpace(string(out))
		if status.ErrorMessage == "" {
			status.ErrorMessage = err.Error()
		}
		return status
	}

	status.Installed = true
	status.Version = parseBrowserVersion(string(out))
	status.MinVersionOK = compareSemver(status.Version, RequiredRuntimeVersion) >= 0
	if !status.MinVersionOK {
		status.ErrorCode = "version_too_old"
		status.ErrorMessage = "gsd-browser " + status.Version + " is older than required " + RequiredRuntimeVersion
		return status
	}

	manifestCtx, manifestCancel := context.WithTimeout(ctx, runtimeProbeStepTimeout)
	defer manifestCancel()
	manifestOut, err := exec.CommandContext(manifestCtx, binaryPath, "cloud-methods", "--json").CombinedOutput()
	if err != nil {
		status.ErrorCode = "manifest_unavailable"
		status.ErrorMessage = strings.TrimSpace(string(manifestOut))
		if status.ErrorMessage == "" {
			status.ErrorMessage = err.Error()
		}
		return status
	}

	var manifest methodManifest
	if err := json.Unmarshal(manifestOut, &manifest); err != nil {
		status.ErrorCode = "manifest_invalid"
		status.ErrorMessage = err.Error()
		return status
	}
	status.CloudMethodsVersion = manifest.ManifestVersion

	healthCtx, healthCancel := context.WithTimeout(ctx, runtimeProbeStepTimeout)
	defer healthCancel()
	healthOut, err := exec.CommandContext(healthCtx, binaryPath, "daemon", "health", "--json").CombinedOutput()
	if err != nil {
		status.ErrorCode = "chrome_missing"
		status.ErrorMessage = strings.TrimSpace(string(healthOut))
		if status.ErrorMessage == "" {
			status.ErrorMessage = err.Error()
		}
		return status
	}
	var health daemonHealth
	if err := json.Unmarshal(healthOut, &health); err != nil || !health.browserAvailable() {
		status.ErrorCode = "chrome_missing"
		if health.Error != "" {
			status.ErrorMessage = health.Error
		} else if err != nil {
			status.ErrorMessage = err.Error()
		} else if health.Session.Reason != "" {
			status.ErrorMessage = health.Session.Reason
		} else {
			status.ErrorMessage = "Chrome/Chromium is unavailable"
		}
		return status
	}
	status.ChromeAvailable = true
	status.Ready = true
	return status
}

func (h daemonHealth) browserAvailable() bool {
	if h.OK && h.ChromeAvailable {
		return true
	}
	return h.Session.BrowserConnected && h.Session.Status == "healthy"
}

func parseBrowserVersion(out string) string {
	re := regexp.MustCompile(`\d+\.\d+\.\d+`)
	if match := re.FindString(out); match != "" {
		return match
	}
	return strings.TrimSpace(out)
}

func compareSemver(a, b string) int {
	parse := func(s string) [3]int {
		var out [3]int
		parts := strings.Split(s, ".")
		for i := 0; i < len(parts) && i < 3; i++ {
			for _, ch := range parts[i] {
				if ch < '0' || ch > '9' {
					break
				}
				out[i] = out[i]*10 + int(ch-'0')
			}
		}
		return out
	}
	av := parse(a)
	bv := parse(b)
	for i := 0; i < 3; i++ {
		if av[i] > bv[i] {
			return 1
		}
		if av[i] < bv[i] {
			return -1
		}
	}
	if a == "" && b != "" {
		return -1
	}
	return 0
}
