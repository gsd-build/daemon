import assert from "node:assert/strict";
import test from "node:test";
import registerExtension, { mergeClaudeCliTools } from "./index.ts";
import { isMinimalToolProfile, toolProfile } from "./tool-policy.js";

function withCapturedStdout(fn) {
  const originalWrite = process.stdout.write;
  const chunks = [];
  process.stdout.write = function write(chunk, encoding, callback) {
    chunks.push(String(chunk));
    if (typeof encoding === "function") encoding();
    if (typeof callback === "function") callback();
    return true;
  };
  try {
    return { result: fn(), output: chunks.join("") };
  } finally {
    process.stdout.write = originalWrite;
  }
}

test("toolProfile defaults to full and detects minimal", () => {
  assert.equal(toolProfile({}), "full");
  assert.equal(toolProfile({ GSD_TOOL_PROFILE: " minimal " }), "minimal");
  assert.equal(isMinimalToolProfile({ GSD_TOOL_PROFILE: "minimal" }), true);
  assert.equal(isMinimalToolProfile({ GSD_TOOL_PROFILE: "full" }), false);
});

test("minimal profile registers providers without tools", () => {
  const previous = process.env.GSD_TOOL_PROFILE;
  const tools = [];
  const providers = [];
  let output = "";
  try {
    process.env.GSD_TOOL_PROFILE = "minimal";
    ({ output } = withCapturedStdout(() => {
      registerExtension({
        registerTool(tool) {
          tools.push(tool);
        },
        registerProvider(name) {
          providers.push(name);
        },
      });
    }));
  } finally {
    if (previous === undefined) {
      delete process.env.GSD_TOOL_PROFILE;
    } else {
      process.env.GSD_TOOL_PROFILE = previous;
    }
  }

  assert.deepEqual(tools, []);
  assert.deepEqual(providers, ["claude-cli", "codex-appserver", "openrouter"]);
  const diagnostics = JSON.parse(output.trim());
  assert.equal(diagnostics.type, "scaffold_diagnostics");
  assert.equal(diagnostics.toolProfile, "minimal");
  assert.equal(diagnostics.registeredToolCount, 0);
  assert.equal(diagnostics.totals.schemaBytes, 0);
});

test("minimal profile hides Claude CLI context tools", () => {
  const previous = process.env.GSD_TOOL_PROFILE;
  try {
    process.env.GSD_TOOL_PROFILE = "minimal";
    const tools = mergeClaudeCliTools([{ name: "ask_human", description: "Ask", parameters: {} }], {
      grantId: "grant",
      browserId: "browser",
      sessionId: "session",
    });
    assert.deepEqual(tools, []);
  } finally {
    if (previous === undefined) {
      delete process.env.GSD_TOOL_PROFILE;
    } else {
      process.env.GSD_TOOL_PROFILE = previous;
    }
  }
});
