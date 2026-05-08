package pi

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	envPiRuntime             = "GSD_PI_RUNTIME"
	envPiStockBinary         = "GSD_PI_STOCK_BINARY"
	envPiRustClaudeProvider  = "GSD_PI_RUST_CLAUDE_PROVIDER"
	envAnthropicAPIKey       = "ANTHROPIC_API_KEY"
	defaultPiRustClaudeAlias = "anthropic"
)

type Runtime string

const (
	RuntimeStock Runtime = "stock"
	RuntimeRust  Runtime = "rust"
)

func (r Runtime) normalize(binaryPath string) Runtime {
	if parsed := parseRuntime(string(r)); parsed != "" {
		return parsed
	}
	return ResolveRuntime(binaryPath)
}

func ResolveRuntime(binaryPath string) Runtime {
	if parsed := parseRuntime(os.Getenv(envPiRuntime)); parsed != "" {
		return parsed
	}
	name := strings.ToLower(filepath.Base(strings.TrimSpace(binaryPath)))
	if strings.Contains(name, "pi-rust") {
		return RuntimeRust
	}
	return RuntimeStock
}

func StockBinaryPath() string {
	if value := strings.TrimSpace(os.Getenv(envPiStockBinary)); value != "" {
		return value
	}
	return "pi"
}

func parseRuntime(value string) Runtime {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "stock", "pi", "pi-mono", "typescript", "ts":
		return RuntimeStock
	case "rust", "pi-rust":
		return RuntimeRust
	default:
		return ""
	}
}

func RuntimeSupportsProvider(runtime Runtime, provider string) bool {
	if runtime != RuntimeRust {
		return true
	}
	switch ProviderOrDefault(provider) {
	case "claude-cli":
		return piRustClaudeProviderAlias() != ""
	case "anthropic",
		"openrouter", "zai", "kimi-coding",
		"openai-responses", "azure-openai-responses", "openai-codex",
		"github-copilot",
		"cloudflare-workers-ai", "cloudflare-ai-gateway",
		"amazon-bedrock",
		"google", "google-vertex",
		"mistral",
		"faux":
		return true
	default:
		return false
	}
}

func providerForRuntime(runtime Runtime, provider string) string {
	provider = ProviderOrDefault(provider)
	if runtime != RuntimeRust {
		return provider
	}
	if provider == "zai" || provider == "kimi-coding" {
		return "openrouter"
	}
	if provider != "claude-cli" {
		return provider
	}
	if alias := piRustClaudeProviderAlias(); alias != "" {
		return alias
	}
	return defaultPiRustClaudeAlias
}

func piRustClaudeProviderAlias() string {
	if alias := strings.TrimSpace(os.Getenv(envPiRustClaudeProvider)); alias != "" {
		return alias
	}
	if strings.TrimSpace(os.Getenv(envAnthropicAPIKey)) == "" {
		return ""
	}
	return defaultPiRustClaudeAlias
}

func (opts Options) runtime() Runtime {
	return opts.Runtime.normalize(opts.BinaryPath)
}
