import assert from "node:assert/strict";
import test from "node:test";
import registerExtension from "./index.ts";

function captureClaudeCliProvider() {
  let provider;
  const pi = {
    registerTool() {},
    registerProvider(name, definition) {
      if (name === "claude-cli") provider = definition;
    },
  };

  registerExtension(pi);
  assert.ok(provider, "claude-cli provider should be registered");
  return provider;
}

test("claude-cli provider registers Claude Haiku 4.5 model metadata", () => {
  const provider = captureClaudeCliProvider();
  const haiku = provider.models.find((model) => model.id === "claude-haiku-4-5-20251001");

  assert.ok(haiku, "Claude Haiku 4.5 should be selectable by model id");
  assert.equal(haiku.name, "Claude Haiku 4.5 (via SDK)");
  assert.equal(haiku.reasoning, false);
  assert.deepEqual(haiku.input, ["text", "image"]);
  assert.deepEqual(haiku.cost, { input: 1.0, output: 5.0, cacheRead: 0.10, cacheWrite: 1.25 });
  assert.equal(haiku.contextWindow, 200_000);
  assert.equal(haiku.maxTokens, 64_000);
});

test("claude-cli provider registers current and previous Claude Opus metadata", () => {
  const provider = captureClaudeCliProvider();
  const opus47 = provider.models.find((model) => model.id === "claude-opus-4-7");
  const opus46 = provider.models.find((model) => model.id === "claude-opus-4-6");

  assert.ok(opus47, "Claude Opus 4.7 should be selectable by model id");
  assert.equal(opus47.name, "Claude Opus 4.7 (via SDK)");
  assert.equal(opus47.reasoning, false);
  assert.deepEqual(opus47.input, ["text", "image"]);
  assert.deepEqual(opus47.cost, { input: 5.0, output: 25.0, cacheRead: 0.50, cacheWrite: 6.25 });
  assert.equal(opus47.contextWindow, 1_000_000);
  assert.equal(opus47.maxTokens, 128_000);

  assert.ok(opus46, "Claude Opus 4.6 should remain selectable by model id");
  assert.equal(opus46.name, "Claude Opus 4.6 (via SDK)");
  assert.deepEqual(opus46.cost, { input: 5.0, output: 25.0, cacheRead: 0.50, cacheWrite: 6.25 });
  assert.equal(opus46.contextWindow, 1_000_000);
  assert.equal(opus46.maxTokens, 128_000);
});
