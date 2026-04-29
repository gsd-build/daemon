import assert from "node:assert/strict";
import test from "node:test";
import {
  OPENROUTER_API_KEY_ENV,
  OPENROUTER_BASE_URL,
  OPENROUTER_PROVIDER_ID,
  openRouterModelDefinitions,
  registerOpenRouterProvider,
} from "./openrouter-provider.ts";

test("openRouterModelDefinitions exposes curated OpenRouter models", () => {
  assert.deepEqual(
    openRouterModelDefinitions.map((model) => model.id),
    [
      "z-ai/glm-4.7-flash",
      "deepseek/deepseek-v3.2",
      "moonshotai/kimi-k2.5",
      "qwen/qwen3-coder",
    ],
  );

  for (const model of openRouterModelDefinitions) {
    assert.equal(model.api, "openai-completions");
    assert.deepEqual(model.input, ["text"]);
    assert.equal(model.compat.openRouterRouting.sort, "throughput");
  }
});

test("registerOpenRouterProvider registers OpenRouter request config", () => {
  const registrations = [];
  const pi = {
    registerProvider(name, definition) {
      registrations.push({ name, definition });
    },
  };

  registerOpenRouterProvider(pi);

  assert.equal(registrations.length, 1);
  assert.equal(registrations[0].name, OPENROUTER_PROVIDER_ID);
  assert.equal(registrations[0].definition.baseUrl, OPENROUTER_BASE_URL);
  assert.equal(registrations[0].definition.apiKey, OPENROUTER_API_KEY_ENV);
  assert.equal(registrations[0].definition.api, "openai-completions");
  assert.equal(registrations[0].definition.headers["HTTP-Referer"], "https://app.gsd.build");
  assert.equal(registrations[0].definition.headers["X-Title"], "GSD");
  assert.deepEqual(registrations[0].definition.models, openRouterModelDefinitions);
});
