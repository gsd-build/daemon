import assert from "node:assert/strict";
import { chmod, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import {
  codexDynamicToolsFromContext,
  codexLatestUserTextFromContext,
  codexModelDefinitions,
  codexNativeToolResultFromItem,
  codexNativeToolStartFromItem,
  codexOutputForModel,
  codexPromptTextFromContext,
  codexUsageFromTokenUsage,
  registerCodexAppServerProvider,
} from "./codex-appserver-provider.ts";

test("codexModelDefinitions exposes cloud-selectable Codex AppServer models", () => {
  assert.deepEqual(
    codexModelDefinitions.map((model) => model.id),
    ["gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.2"],
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

test("codexUsageFromTokenUsage preserves cache accounting fields", () => {
  assert.deepEqual(codexUsageFromTokenUsage({
    total: {
      inputTokens: 100,
      outputTokens: 12,
      cacheReadInputTokens: 30,
      cacheCreationInputTokens: 70,
      totalTokens: 212,
    },
  }), {
    input: 100,
    output: 12,
    cacheRead: 30,
    cacheWrite: 70,
    totalTokens: 212,
  });
});

test("codexUsageFromTokenUsage accepts alternate provider field names", () => {
  assert.deepEqual(codexUsageFromTokenUsage({
    input_tokens: 10,
    output_tokens: 2,
    cachedInputTokens: 4,
    cache_creation_input_tokens: 8,
  }), {
    input: 10,
    output: 2,
    cacheRead: 4,
    cacheWrite: 8,
    totalTokens: 24,
  });
});

test("codexPromptTextFromContext renders the active Pi conversation history", () => {
  const prompt = codexPromptTextFromContext({
    messages: [
      { role: "user", content: [{ type: "text", text: "Inspect this repo." }] },
      {
        role: "assistant",
        content: [
          { type: "text", text: "I will read the files." },
          { type: "toolCall", id: "tool-1", name: "shell", arguments: { command: "ls" } },
          { type: "text", text: "I found two entries." },
        ],
      },
      {
        role: "toolResult",
        toolCallId: "tool-1",
        toolName: "shell",
        isError: false,
        content: [{ type: "text", text: "package.json\nsrc" }],
      },
      { role: "user", content: [{ type: "text", text: "What did you find?" }] },
    ],
  });

  assert.match(prompt, /Conversation history from the active GSD session/);
  assert.match(prompt, /User:\nInspect this repo\./);
  assert.match(prompt, /Assistant:\nI will read the files\./);
  assert.match(prompt, /Assistant tool call:\nname: shell\narguments: \{"command":"ls"\}/);
  assert.match(prompt, /Assistant:\nI found two entries\./);
  assert.match(prompt, /Tool result \(shell success\):\npackage\.json\nsrc/);
  assert.match(prompt, /User:\nWhat did you find\?/);
  assert.match(prompt, /Continue from the latest user message\./);
  assert.ok(prompt.indexOf("I will read the files.") < prompt.indexOf("Assistant tool call:"));
  assert.ok(prompt.indexOf("Assistant tool call:") < prompt.indexOf("I found two entries."));
});

test("codexPromptTextFromContext sends a single user turn as plain input", () => {
  const prompt = codexPromptTextFromContext({
    messages: [{ role: "user", content: [{ type: "text", text: "Hello" }] }],
  });

  assert.equal(prompt, "Hello");
});

test("codexLatestUserTextFromContext returns only the incremental user input", () => {
  const text = codexLatestUserTextFromContext({
    messages: [
      { role: "user", content: [{ type: "text", text: "First" }] },
      { role: "assistant", content: [{ type: "text", text: "Reply" }] },
      {
        role: "user",
        content: [
          { type: "text", text: "Second" },
          { type: "text", text: "line" },
        ],
      },
    ],
  });

  assert.equal(text, "Secondline");
});

test("codexNativeToolStartFromItem maps Codex command executions", () => {
  const start = codexNativeToolStartFromItem({
    id: "cmd-1",
    type: "commandExecution",
    command: "npm test",
    cwd: "/tmp/project",
  });

  assert.deepEqual(start, {
    itemId: "cmd-1",
    toolCallId: "cmd-1",
    toolName: "shell",
    args: { command: "npm test", cwd: "/tmp/project" },
  });
});

test("codexNativeToolStartFromItem uses stable fallback IDs for id-less command executions", () => {
  const started = codexNativeToolStartFromItem({
    type: "commandExecution",
    command: "npm test",
    cwd: "/tmp/project",
  }, 0);
  const completed = codexNativeToolStartFromItem({
    type: "commandExecution",
    command: "npm test",
    cwd: "/tmp/project",
    status: "completed",
    aggregatedOutput: "ok",
  }, 7);

  assert.ok(started?.itemId.startsWith("cmd_"));
  assert.equal(started?.itemId, completed?.itemId);
  assert.equal(started?.toolCallId, completed?.toolCallId);
});

test("codexNativeToolStartFromItem maps Codex file changes", () => {
  const start = codexNativeToolStartFromItem({
    id: "file-1",
    type: "fileChange",
    changes: [{ path: "src/index.ts", kind: "update", diff: "@@ patch" }],
  });

  assert.deepEqual(start, {
    itemId: "file-1",
    toolCallId: "file-1",
    toolName: "file_change",
    args: { changes: [{ path: "src/index.ts", kind: "update" }] },
  });
});

test("codexNativeToolStartFromItem uses stable fallback IDs for id-less file changes", () => {
  const started = codexNativeToolStartFromItem({
    type: "fileChange",
    changes: [{ path: "src/index.ts", kind: "update" }],
  }, 0);
  const completed = codexNativeToolStartFromItem({
    type: "fileChange",
    status: "completed",
    changes: [{ path: "src/index.ts", kind: "update", diff: "@@ patch" }],
  }, 9);

  assert.ok(started?.itemId.startsWith("file_"));
  assert.equal(started?.itemId, completed?.itemId);
  assert.equal(started?.toolCallId, completed?.toolCallId);
});

test("codexNativeToolStartFromItem maps path-only file changes", () => {
  const start = codexNativeToolStartFromItem({
    id: "file-legacy",
    type: "fileChange",
    path: "src/legacy.ts",
  });

  assert.deepEqual(start, {
    itemId: "file-legacy",
    toolCallId: "file-legacy",
    toolName: "file_change",
    args: { changes: [{ path: "src/legacy.ts", kind: "" }] },
  });
});

test("codexNativeToolStartFromItem maps Codex MCP tool calls", () => {
  const start = codexNativeToolStartFromItem({
    id: "mcp-1",
    type: "mcpToolCall",
    tool: "request_user_input",
    arguments: { questions: [{ id: "scope", question: "Pick one" }] },
  });

  assert.deepEqual(start, {
    itemId: "mcp-1",
    toolCallId: "mcp-1",
    toolName: "request_user_input",
    args: { questions: [{ id: "scope", question: "Pick one" }] },
  });
});

test("codexNativeToolResultFromItem maps command completion status and output", () => {
  const result = codexNativeToolResultFromItem({
    type: "commandExecution",
    status: "failed",
    exitCode: 2,
    durationMs: 125,
    aggregatedOutput: "typecheck failed",
  });

  assert.deepEqual(result, {
    resultText: "typecheck failed",
    details: { exitCode: 2, durationMs: 125, status: "failed" },
    isError: true,
  });
});

test("codexNativeToolResultFromItem maps MCP text results", () => {
  const result = codexNativeToolResultFromItem({
    type: "mcpToolCall",
    status: "completed",
    server: "node_repl",
    durationMs: 12,
    result: {
      content: [
        { type: "text", text: "first" },
        { type: "text", text: "second" },
      ],
    },
  });

  assert.deepEqual(result, {
    resultText: "first\nsecond",
    details: { server: "node_repl", durationMs: 12, status: "completed" },
    isError: false,
  });
});

async function collectStream(stream) {
  const events = [];
  for await (const event of stream) {
    events.push(event);
  }
  return events;
}

function summarizeFakeCodexStats(contents) {
  const records = contents
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
  const count = (event) => records.filter((record) => record.event === event).length;
  return {
    processStarts: count("processStart"),
    initializes: count("initialize"),
    threadStarts: count("threadStart"),
    turnStarts: count("turnStart"),
    threadStartsDetails: records.filter((record) => record.event === "threadStart"),
    threadIds: [...new Set(records.filter((record) => record.event === "threadStart").map((record) => record.threadId))],
  };
}

test("codex appserver provider keeps one process and thread across two turns", async () => {
  const dir = await mkdtemp(path.join(tmpdir(), "fake-codex-appserver-"));
  const statsFile = path.join(dir, "stats.ndjson");
  const codexBin = path.join(dir, "codex");
  await writeFile(statsFile, "");
  await writeFile(codexBin, `#!/usr/bin/env node
const { appendFileSync } = await import("node:fs");
const { createInterface } = await import("node:readline");

const statsFile = process.env.FAKE_CODEX_STATS_FILE;
let turnCount = 0;
const threadId = "thread_fake_warm";
const record = (event, data = {}) => {
  appendFileSync(statsFile, JSON.stringify({ event, ...data }) + "\\n");
};
const send = (message) => {
  process.stdout.write(JSON.stringify(message) + "\\n");
};

record("processStart");

createInterface({ input: process.stdin }).on("line", (line) => {
  const message = JSON.parse(line);
  if (message.method === "initialize") {
    record("initialize");
    send({ id: message.id, result: {} });
    return;
  }
  if (message.method === "initialized") {
    return;
  }
  if (message.method === "thread/start") {
    record("threadStart", {
      threadId,
      sandbox: message.params.sandbox,
      approvalPolicy: message.params.approvalPolicy,
    });
    send({ id: message.id, result: { thread: { id: threadId } } });
    return;
  }
  if (message.method === "turn/start") {
    turnCount += 1;
    const turnId = "turn_" + turnCount;
    const itemId = "item_" + turnCount;
    record("turnStart", { threadId: message.params.threadId, turnId });
    send({ id: message.id, result: { turn: { id: turnId } } });
    send({ method: "turn/started", params: { turn: { id: turnId } } });
    send({ method: "item/started", params: { item: { id: itemId, type: "agentMessage" } } });
    send({ method: "item/agentMessage/delta", params: { itemId, delta: "reply-" + turnCount } });
    send({ method: "item/completed", params: { item: { id: itemId, type: "agentMessage" } } });
    send({ method: "turn/completed", params: { turn: { id: turnId, status: "completed" } } });
    if (turnCount === 2) {
      setTimeout(() => process.exit(0), 20);
    }
  }
});
`);
  await chmod(codexBin, 0o700);

  const previousPath = process.env.PATH;
  const previousStats = process.env.FAKE_CODEX_STATS_FILE;
  process.env.PATH = `${dir}${path.delimiter}${previousPath ?? ""}`;
  process.env.FAKE_CODEX_STATS_FILE = statsFile;
  try {
    let provider;
    registerCodexAppServerProvider({
      registerProvider(name, definition) {
        if (name === "codex-appserver") provider = definition;
      },
    });

    const model = { id: "gpt-5.5", api: "openai-responses", provider: "codex-appserver" };
    const firstEvents = await collectStream(provider.streamSimple(model, {
      messages: [{ role: "user", content: [{ type: "text", text: "first" }] }],
      tools: [],
    }));
    const secondEvents = await collectStream(provider.streamSimple(model, {
      messages: [
        { role: "user", content: [{ type: "text", text: "first" }] },
        { role: "assistant", content: [{ type: "text", text: "reply-1" }] },
        { role: "user", content: [{ type: "text", text: "second" }] },
      ],
      tools: [],
    }));

    assert.equal(firstEvents.at(-1)?.type, "done");
    assert.equal(secondEvents.at(-1)?.type, "done");

    const summary = summarizeFakeCodexStats(await readFile(statsFile, "utf8"));
    assert.equal(summary.processStarts, 1);
    assert.equal(summary.initializes, 1);
    assert.equal(summary.threadStarts, 1);
    assert.equal(summary.turnStarts, 2);
    assert.equal(summary.threadStartsDetails[0]?.sandbox, "workspace-write");
    assert.equal(summary.threadStartsDetails[0]?.approvalPolicy, "on-request");
    assert.deepEqual(summary.threadIds, ["thread_fake_warm"]);
  } finally {
    if (previousPath === undefined) delete process.env.PATH;
    else process.env.PATH = previousPath;
    if (previousStats === undefined) delete process.env.FAKE_CODEX_STATS_FILE;
    else process.env.FAKE_CODEX_STATS_FILE = previousStats;
  }
});

test("codex appserver full access requires explicit local grant", async () => {
  const dir = await mkdtemp(path.join(tmpdir(), "fake-codex-appserver-grant-"));
  const statsFile = path.join(dir, "stats.ndjson");
  const codexBin = path.join(dir, "codex");
  await writeFile(statsFile, "");
  await writeFile(codexBin, `#!/usr/bin/env node
const { appendFileSync } = await import("node:fs");
const { createInterface } = await import("node:readline");
const statsFile = process.env.FAKE_CODEX_STATS_FILE;
const send = (message) => process.stdout.write(JSON.stringify(message) + "\\n");
createInterface({ input: process.stdin }).on("line", (line) => {
  const message = JSON.parse(line);
  if (message.method === "initialize") {
    send({ id: message.id, result: {} });
    return;
  }
  if (message.method === "initialized") return;
  if (message.method === "thread/start") {
    appendFileSync(statsFile, JSON.stringify({
      sandbox: message.params.sandbox,
      approvalPolicy: message.params.approvalPolicy,
    }) + "\\n");
    send({ id: message.id, result: { thread: { id: "thread_grant" } } });
    return;
  }
  if (message.method === "turn/start") {
    send({ id: message.id, result: { turn: { id: "turn_1" } } });
    send({ method: "turn/started", params: { turn: { id: "turn_1" } } });
    send({ method: "item/started", params: { item: { id: "item_1", type: "agentMessage" } } });
    send({ method: "item/agentMessage/delta", params: { itemId: "item_1", delta: "ok" } });
    send({ method: "item/completed", params: { item: { id: "item_1", type: "agentMessage" } } });
    send({ method: "turn/completed", params: { turn: { id: "turn_1", status: "completed" } } });
    setTimeout(() => process.exit(0), 20);
  }
});
`);
  await chmod(codexBin, 0o700);

  const previousPath = process.env.PATH;
  const previousStats = process.env.FAKE_CODEX_STATS_FILE;
  const previousGrant = process.env.GSD_CODEX_FULL_ACCESS_GRANT;
  process.env.PATH = `${dir}${path.delimiter}${previousPath ?? ""}`;
  process.env.FAKE_CODEX_STATS_FILE = statsFile;
  process.env.GSD_CODEX_FULL_ACCESS_GRANT = "allow-danger-full-access";
  try {
    let provider;
    registerCodexAppServerProvider({
      registerProvider(name, definition) {
        if (name === "codex-appserver") provider = definition;
      },
    });

    const model = { id: "gpt-5.5", api: "openai-responses", provider: "codex-appserver" };
    await collectStream(provider.streamSimple(model, {
      messages: [{ role: "user", content: [{ type: "text", text: "first" }] }],
      tools: [],
    }));

    const records = (await readFile(statsFile, "utf8")).trim().split("\n").map((line) => JSON.parse(line));
    assert.equal(records[0]?.sandbox, "danger-full-access");
    assert.equal(records[0]?.approvalPolicy, "never");
  } finally {
    if (previousPath === undefined) delete process.env.PATH;
    else process.env.PATH = previousPath;
    if (previousStats === undefined) delete process.env.FAKE_CODEX_STATS_FILE;
    else process.env.FAKE_CODEX_STATS_FILE = previousStats;
    if (previousGrant === undefined) delete process.env.GSD_CODEX_FULL_ACCESS_GRANT;
    else process.env.GSD_CODEX_FULL_ACCESS_GRANT = previousGrant;
  }
});

test("codex appserver provider surfaces empty completed turns as errors", async () => {
  const dir = await mkdtemp(path.join(tmpdir(), "fake-codex-appserver-empty-"));
  const codexBin = path.join(dir, "codex");
  await writeFile(codexBin, `#!/usr/bin/env node
const { createInterface } = await import("node:readline");
const send = (message) => process.stdout.write(JSON.stringify(message) + "\\n");
createInterface({ input: process.stdin }).on("line", (line) => {
  const message = JSON.parse(line);
  if (message.method === "initialize") {
    send({ id: message.id, result: {} });
    return;
  }
  if (message.method === "initialized") return;
  if (message.method === "thread/start") {
    send({ id: message.id, result: { thread: { id: "thread_empty" } } });
    return;
  }
  if (message.method === "turn/start") {
    process.stderr.write("selected model produced no events\\n");
    send({ id: message.id, result: { turn: { id: "turn_empty" } } });
    setTimeout(() => {
      send({ method: "turn/completed", params: { turn: { id: "turn_empty", status: "completed" } } });
    }, 20);
    setTimeout(() => process.exit(0), 60);
  }
});
`);
  await chmod(codexBin, 0o700);

  const previousPath = process.env.PATH;
  process.env.PATH = `${dir}${path.delimiter}${previousPath ?? ""}`;
  try {
    let provider;
    registerCodexAppServerProvider({
      registerProvider(name, definition) {
        if (name === "codex-appserver") provider = definition;
      },
    });

    const model = { id: "gpt-5.4-mini", api: "openai-responses", provider: "codex-appserver" };
    const events = await collectStream(provider.streamSimple(model, {
      messages: [{ role: "user", content: [{ type: "text", text: "first" }] }],
      tools: [],
    }));

    assert.equal(events.at(-1)?.type, "error");
    assert.match(events.at(-1)?.error?.errorMessage, /completed without assistant content/);
    assert.match(events.at(-1)?.error?.errorMessage, /selected model produced no events/);
  } finally {
    if (previousPath === undefined) delete process.env.PATH;
    else process.env.PATH = previousPath;
  }
});
