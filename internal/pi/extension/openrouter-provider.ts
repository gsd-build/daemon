import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";

export const OPENROUTER_PROVIDER_ID = "openrouter";
export const OPENROUTER_BASE_URL = "https://openrouter.ai/api/v1";
export const OPENROUTER_API_KEY_ENV = "OPENROUTER_API_KEY";

export const openRouterModelDefinitions = [
  {
    id: "z-ai/glm-4.7-flash",
    name: "GLM Flash",
    api: "openai-completions" as const,
    reasoning: true,
    input: ["text"] as const,
    cost: { input: 0.06, output: 0.4, cacheRead: 0.01, cacheWrite: 0 },
    contextWindow: 202_752,
    maxTokens: 16_384,
    compat: {
      openRouterRouting: { sort: "throughput" },
    },
  },
  {
    id: "deepseek/deepseek-v3.2",
    name: "DeepSeek V3.2",
    api: "openai-completions" as const,
    reasoning: true,
    input: ["text"] as const,
    cost: { input: 0.252, output: 0.378, cacheRead: 0.0252, cacheWrite: 0 },
    contextWindow: 131_072,
    maxTokens: 65_536,
    compat: {
      openRouterRouting: { sort: "throughput" },
    },
  },
  {
    id: "moonshotai/kimi-k2.5",
    name: "Kimi K2.5",
    api: "openai-completions" as const,
    reasoning: true,
    input: ["text"] as const,
    cost: { input: 0.44, output: 2.0, cacheRead: 0.22, cacheWrite: 0 },
    contextWindow: 262_144,
    maxTokens: 65_535,
    compat: {
      openRouterRouting: { sort: "throughput" },
    },
  },
  {
    id: "qwen/qwen3-coder",
    name: "Qwen Coder",
    api: "openai-completions" as const,
    reasoning: false,
    input: ["text"] as const,
    cost: { input: 0.22, output: 1.8, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 262_144,
    maxTokens: 65_536,
    compat: {
      openRouterRouting: { sort: "throughput" },
    },
  },
] as const;

export function registerOpenRouterProvider(pi: ExtensionAPI) {
  pi.registerProvider(OPENROUTER_PROVIDER_ID, {
    baseUrl: OPENROUTER_BASE_URL,
    apiKey: OPENROUTER_API_KEY_ENV,
    api: "openai-completions",
    headers: {
      "HTTP-Referer": "https://app.gsd.build",
      "X-Title": "GSD",
    },
    models: openRouterModelDefinitions as any,
  });
}
