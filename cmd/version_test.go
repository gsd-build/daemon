package cmd

import (
	"strings"
	"testing"
)

func TestVersionVariablesAreSettable(t *testing.T) {
	// These are package-level vars that ldflags can override.
	// We just verify they exist and have non-empty defaults.
	if Version == "" {
		t.Errorf("Version should have a non-empty default")
	}
	if Commit == "" {
		t.Errorf("Commit should have a non-empty default (e.g. 'unknown')")
	}
	if BuildDate == "" {
		t.Errorf("BuildDate should have a non-empty default")
	}
}

func TestVersionStringIncludesAllFields(t *testing.T) {
	out := versionString()
	if !strings.Contains(out, "gsd-cloud") {
		t.Errorf("versionString should include 'gsd-cloud', got %q", out)
	}
	if !strings.Contains(out, Version) {
		t.Errorf("versionString should include Version, got %q", out)
	}
	if !strings.Contains(out, Commit) {
		t.Errorf("versionString should include Commit, got %q", out)
	}
	if !strings.Contains(out, BuildDate) {
		t.Errorf("versionString should include BuildDate, got %q", out)
	}
}
