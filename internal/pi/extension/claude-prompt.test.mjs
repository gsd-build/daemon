import assert from "node:assert/strict";
import test from "node:test";
import {
  buildClaudePromptMessages,
  deriveClaudeSdkSessionId,
  externalPiToolAcknowledgement,
  finalizeActivePiToolCall,
} from "./index.ts";

const userMessage = {
  role: "user",
  content: [{ type: "text", text: "Inspect the project." }],
  timestamp: 1,
};

const assistantToolCall = {
  role: "assistant",
  content: [{
    type: "toolCall",
    id: "toolu_123",
    name: "bash",
    arguments: { command: "pwd" },
  }],
  api: "anthropic-messages",
  provider: "claude-cli",
  model: "claude-sonnet-4-6",
  usage: {
    input: 0,
    output: 0,
    cacheRead: 0,
    cacheWrite: 0,
    totalTokens: 0,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  },
  stopReason: "toolUse",
  timestamp: 2,
};

const toolResult = {
  role: "toolResult",
  toolCallId: "toolu_123",
  toolName: "bash",
  content: [{ type: "text", text: "/tmp/project" }],
  isError: false,
  timestamp: 3,
};

test("buildClaudePromptMessages sends only the latest message for a resumed SDK session", () => {
  const messages = buildClaudePromptMessages(
    [userMessage, assistantToolCall, toolResult],
    "11111111-1111-5111-8111-111111111111",
    false,
  );

  assert.equal(messages.length, 1);
  assert.equal(messages[0].message.role, "user");
  assert.equal(messages[0].parent_tool_use_id, null);
  assert.equal(messages[0].message.content[0].type, "tool_result");
  assert.equal(messages[0].message.content[0].tool_use_id, "toolu_123");
  assert.equal(messages[0].message.content[0].content[0].text, "/tmp/project");
  assert.equal("isReplay" in messages[0], false);
});

test("buildClaudePromptMessages can bootstrap a missing SDK session from Pi history", () => {
  const messages = buildClaudePromptMessages(
    [userMessage, assistantToolCall, toolResult],
    "11111111-1111-5111-8111-111111111111",
    true,
  );

  assert.equal(messages.length, 1);
  assert.equal(messages[0].message.role, "user");
  assert.match(messages[0].message.content, /User: Inspect the project\./);
  assert.match(messages[0].message.content, /Assistant tool call: bash/);
  assert.match(messages[0].message.content, /Tool result \(bash success\):/);
  assert.match(messages[0].message.content, /Continue from the latest message/);
});

test("deriveClaudeSdkSessionId is stable and scoped", () => {
  const first = deriveClaudeSdkSessionId("018f2d1a-7f1e-7000-9000-000000000001", "/work/a");
  const second = deriveClaudeSdkSessionId("018f2d1a-7f1e-7000-9000-000000000001", "/work/a");
  const other = deriveClaudeSdkSessionId("018f2d1a-7f1e-7000-9000-000000000001", "/work/b");

  assert.equal(first, second);
  assert.notEqual(first, other);
  assert.match(first, /^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
});

test("finalizeActivePiToolCall uses the completed streamed JSON arguments", () => {
  assert.deepEqual(finalizeActivePiToolCall({
    idx: 0,
    id: "toolu_789",
    name: "plan_update_item",
    jsonAcc: "{\"status\":\"completed\"}",
  }, {}), {
    type: "toolCall",
    id: "toolu_789",
    name: "plan_update_item",
    arguments: { status: "completed" },
  });

  assert.equal(finalizeActivePiToolCall(null, {}), null);
});

test("finalizeActivePiToolCall rejects malformed streamed JSON arguments", () => {
  assert.throws(() => finalizeActivePiToolCall({
    idx: 0,
    id: "toolu_bad",
    name: "plan_update_item",
    jsonAcc: "{\"status\":",
  }, {}), /Failed to parse tool_use input for plan_update_item/);

  assert.throws(() => finalizeActivePiToolCall({
    idx: 0,
    id: "toolu_array",
    name: "plan_update_item",
    jsonAcc: "[\"completed\"]",
  }, {}), /tool_use input must be a JSON object/);
});

test("externalPiToolAcknowledgement satisfies the SDK tool call without surfacing an error", () => {
  const result = externalPiToolAcknowledgement();

  assert.equal(result.isError, false);
  assert.match(result.content[0].text, /daemon/);
});
