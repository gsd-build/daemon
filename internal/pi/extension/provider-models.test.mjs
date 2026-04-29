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
