import assert from "node:assert/strict";
import test from "node:test";
import {
  codexDynamicToolsFromContext,
  codexModelDefinitions,
  codexOutputForModel,
} from "./codex-appserver-provider.ts";

test("codexModelDefinitions exposes GPT-5.5 and GPT-5.4", () => {
  assert.deepEqual(
    codexModelDefinitions.map((model) => model.id),
    ["gpt-5.5", "gpt-5.4"],
  );
  for (const model of codexModelDefinitions) {
    assert.equal(model.provider, "codex-appserver");
    assert.equal(model.api, "openai-responses");
  }
});

test("codexDynamicToolsFromContext maps Pi tools into the gsd namespace", () => {
  const dynamicTools = codexDynamicToolsFromContext({
    tools: [{
      name: "ask_user_questions",
      label: "Ask User Questions",
      description: "Ask structured questions.",
      parameters: {
        type: "object",
        properties: {
          questions: { type: "array", items: { type: "object" } },
        },
        required: ["questions"],
      },
    }],
  });

  assert.equal(dynamicTools.length, 1);
  assert.equal(dynamicTools[0].namespace, "gsd");
  assert.equal(dynamicTools[0].name, "ask_user_questions");
  assert.equal(dynamicTools[0].exposeToContext, true);
  assert.equal(dynamicTools[0].inputSchema.properties.questions.type, "array");
});

test("codexOutputForModel stamps provider and empty usage", () => {
  const output = codexOutputForModel({ id: "gpt-5.5", api: "openai-responses" });
  assert.equal(output.provider, "codex-appserver");
  assert.equal(output.model, "gpt-5.5");
  assert.equal(output.usage.input, 0);
  assert.equal(output.usage.output, 0);
  assert.equal(output.usage.cacheRead, 0);
  assert.equal(output.usage.cacheWrite, 0);
});
