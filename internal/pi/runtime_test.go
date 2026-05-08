package pi

import "testing"

func TestResolveRuntimeUsesExplicitEnv(t *testing.T) {
	t.Setenv(envPiRuntime, "rust")
	if got := ResolveRuntime("/tmp/pi"); got != RuntimeRust {
		t.Fatalf("ResolveRuntime explicit rust = %q, want %q", got, RuntimeRust)
	}

	t.Setenv(envPiRuntime, "stock")
	if got := ResolveRuntime("/tmp/pi-rust"); got != RuntimeStock {
		t.Fatalf("ResolveRuntime explicit stock = %q, want %q", got, RuntimeStock)
	}
}

func TestProviderForPiRustClaudeAliasCanBeOverridden(t *testing.T) {
	t.Setenv(envPiRustClaudeProvider, "openrouter")
	if got := providerForRuntime(RuntimeRust, "claude-cli"); got != "openrouter" {
		t.Fatalf("providerForRuntime pi-rust claude alias = %q, want openrouter", got)
	}
	if got := providerForRuntime(RuntimeStock, "claude-cli"); got != "claude-cli" {
		t.Fatalf("providerForRuntime stock claude = %q, want claude-cli", got)
	}
	if got := providerForRuntime(RuntimeRust, "anthropic"); got != "anthropic" {
		t.Fatalf("providerForRuntime keeps explicit pi-rust provider = %q, want anthropic", got)
	}
}

func TestProviderForPiRustMapsOpenRouterAliases(t *testing.T) {
	for _, provider := range []string{"zai", "kimi-coding"} {
		if got := providerForRuntime(RuntimeRust, provider); got != "openrouter" {
			t.Fatalf("providerForRuntime pi-rust %s = %q, want openrouter", provider, got)
		}
	}
}

func TestRuntimeSupportsPiRustProviderSet(t *testing.T) {
	for _, provider := range []string{"anthropic", "openrouter", "zai", "kimi-coding", "openai-codex", "github-copilot", "faux"} {
		if !RuntimeSupportsProvider(RuntimeRust, provider) {
			t.Fatalf("RuntimeSupportsProvider(pi-rust, %s) = false, want true", provider)
		}
	}
	for _, provider := range []string{"codex-appserver"} {
		if RuntimeSupportsProvider(RuntimeRust, provider) {
			t.Fatalf("RuntimeSupportsProvider(pi-rust, %s) = true, want false", provider)
		}
	}
}

func TestRuntimeSupportsPiRustClaudeCliWhenCredentialed(t *testing.T) {
	if RuntimeSupportsProvider(RuntimeRust, "claude-cli") {
		t.Fatal("RuntimeSupportsProvider(pi-rust, claude-cli) = true without alias or Anthropic key, want false")
	}
	t.Setenv(envAnthropicAPIKey, "sk-test")
	if !RuntimeSupportsProvider(RuntimeRust, "claude-cli") {
		t.Fatal("RuntimeSupportsProvider(pi-rust, claude-cli) = false with Anthropic key, want true")
	}
}
