package terminal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateCWDResolvesDirectory(t *testing.T) {
	dir := t.TempDir()
	resolved, err := ValidateCWD(dir)
	if err != nil {
		t.Fatalf("ValidateCWD: %v", err)
	}
	if resolved == "" || !filepath.IsAbs(resolved) {
		t.Fatalf("resolved cwd = %q", resolved)
	}
}

func TestValidateCWDDefaultsToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	resolved, err := ValidateCWD("")
	if err != nil {
		t.Fatalf("ValidateCWD: %v", err)
	}
	if resolved != home {
		t.Fatalf("resolved = %q, want %q", resolved, home)
	}
}

func TestValidateCWDRejectsRelativePath(t *testing.T) {
	if _, err := ValidateCWD("relative"); err == nil {
		t.Fatal("expected relative cwd to be rejected")
	}
}

func TestResolveShellUsesExecutableShellEnv(t *testing.T) {
	tmp := t.TempDir()
	shell := filepath.Join(tmp, "shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)

	got := ResolveShell()
	if got != shell {
		t.Fatalf("shell = %q, want %q", got, shell)
	}
}
