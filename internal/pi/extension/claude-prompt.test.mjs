import assert from "node:assert/strict";
import test from "node:test";
import {
  buildClaudePromptMessages,
  deriveClaudeSdkSessionId,
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
  assert.equal(messages[0].parent_tool_use_id, "toolu_123");
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

  assert.equal(messages.length, 3);
  assert.equal(messages[0].isReplay, true);
  assert.equal(messages[0].shouldQuery, false);
  assert.equal(messages[0].message.role, "user");
  assert.equal(messages[1].isReplay, true);
  assert.equal(messages[1].shouldQuery, false);
  assert.equal(messages[1].message.role, "assistant");
  assert.equal(messages[1].message.content[0].type, "tool_use");
  assert.equal(messages[1].message.content[0].id, "toolu_123");
  assert.equal("isReplay" in messages[2], false);
  assert.equal(messages[2].message.content[0].type, "tool_result");
});

test("deriveClaudeSdkSessionId is stable and scoped", () => {
  const first = deriveClaudeSdkSessionId("018f2d1a-7f1e-7000-9000-000000000001", "/work/a");
  const second = deriveClaudeSdkSessionId("018f2d1a-7f1e-7000-9000-000000000001", "/work/a");
  const other = deriveClaudeSdkSessionId("018f2d1a-7f1e-7000-9000-000000000001", "/work/b");

  assert.equal(first, second);
  assert.notEqual(first, other);
  assert.match(first, /^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
});
