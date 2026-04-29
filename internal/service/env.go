package service

import (
	"fmt"
	"os"
	"strings"
)

// ProviderEnvironmentKeys are provider credentials and routing variables that
// service-managed daemon processes need in their environment.
var ProviderEnvironmentKeys = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_OAUTH_TOKEN",
	"OPENAI_API_KEY",
	"AZURE_OPENAI_API_KEY",
	"AZURE_OPENAI_BASE_URL",
	"AZURE_OPENAI_RESOURCE_NAME",
	"AZURE_OPENAI_API_VERSION",
	"AZURE_OPENAI_DEPLOYMENT_NAME_MAP",
	"GEMINI_API_KEY",
	"GROQ_API_KEY",
	"CEREBRAS_API_KEY",
	"XAI_API_KEY",
	"OPENROUTER_API_KEY",
	"AI_GATEWAY_API_KEY",
	"ZAI_API_KEY",
	"MISTRAL_API_KEY",
	"MINIMAX_API_KEY",
	"OPENCODE_API_KEY",
	"KIMI_API_KEY",
	"AWS_PROFILE",
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_BEARER_TOKEN_BEDROCK",
	"AWS_REGION",
}

type envSetter func(key string, value string) error

func syncCurrentEnvironment(keys []string, set envSetter) ([]string, error) {
	synced := make([]string, 0, len(keys))
	for _, key := range keys {
		value, ok := currentEnvironmentValue(key)
		if !ok {
			continue
		}
		if err := set(key, value); err != nil {
			return synced, fmt.Errorf("sync %s: %w", key, err)
		}
		synced = append(synced, key)
	}
	return synced, nil
}

func currentEnvironmentValue(key string) (string, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	return value, value != ""
}
