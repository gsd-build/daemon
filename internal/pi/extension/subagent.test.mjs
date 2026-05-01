import test from "node:test";
import assert from "node:assert/strict";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { registerSubagentTool, runChildAgent, subagentProviderForModel } from "./subagent.js";
import { isToolAllowed, parseAllowedTools } from "./tool-policy.js";

function snapshot(name, overrides = {}) {
  return {
    id: `${name}-id`,
    name,
    description: `${name} agent`,
    systemPrompt: "Cloud snapshot prompt",
    model: "claude-haiku-4-5-20251001",
    tools: ["read"],
    versionHash: `${name}-hash`,
    ...overrides,
  };
}

test("registerSubagentTool is gated by daemon subagent env", () => {
  const tools = [];
  const registered = registerSubagentTool(
    { registerTool: (tool) => tools.push(tool) },
    {},
    {},
  );
  assert.equal(registered, false);
  assert.equal(tools.length, 0);
});

test("subagent tool creates child session, runs child, and finalizes usage", async () => {
  const tools = [];
  const calls = [];
  const rpc = {
    async createChild(body) {
      calls.push(["create", body]);
      return {
        runId: "run-1",
        childSessionId: "child-1",
        parentSessionId: body.parentSessionId,
        projectId: "project-1",
        parentToolCallId: body.parentToolCallId,
        runIndex: body.runIndex,
        mode: body.mode,
        agent: snapshot(body.agentName),
      };
    },
    async finalize(body) {
      calls.push(["finalize", body]);
      return {
        ok: true,
        status: "succeeded",
        runId: body.runId,
        childSessionId: body.childSessionId,
        parentSessionId: "parent-1",
        projectId: "project-1",
      };
    },
  };
  registerSubagentTool(
    { registerTool: (tool) => tools.push(tool) },
    {
      GSD_DAEMON_SOCKET: "/tmp/daemon.sock",
      GSD_PARENT_SESSION_ID: "parent-1",
      GSD_AGENT_DIR: "/tmp/agents",
      GSD_SUBAGENT_AUTH_TOKEN: "token",
    },
    {
      rpc,
      async runChildAgent({ agent, runId }) {
        assert.equal(agent.systemPrompt, "Cloud snapshot prompt");
        assert.equal(runId, "run-1");
        return {
          finalText: "Mapped the flow.",
          usage: { input: 10, output: 4, cost: 0.000014, turns: 1 },
        };
      },
    },
  );

  const result = await tools[0].execute("toolu_1", {
    agentName: "explorer",
    task: "Map the flow.",
  });

  assert.equal(result.isError, false);
  assert.equal(result.details.childSessionId, "child-1");
  assert.deepEqual(calls, [
    [
      "create",
      {
        parentSessionId: "parent-1",
        parentToolCallId: "toolu_1",
        runIndex: 1,
        mode: "single",
        agentName: "explorer",
        task: "Map the flow.",
      },
    ],
    [
      "finalize",
      {
        runId: "run-1",
        childSessionId: "child-1",
        status: "done",
        totalInputTokens: 10,
        totalOutputTokens: 4,
        totalCostUsd: "0.000014",
        turnCount: 1,
        finalText: "Mapped the flow.",
      },
    ],
  ]);
});

test("subagentProviderForModel routes configured model families to their Pi provider", () => {
  assert.equal(subagentProviderForModel("claude-haiku-4-5-20251001"), "claude-cli");
  assert.equal(subagentProviderForModel("z-ai/glm-4.7-flash"), "openrouter");
  assert.equal(subagentProviderForModel("gpt-5.5"), "codex-appserver");
});

test("subagent tool runs parallel child tasks and aggregates details", async () => {
  const tools = [];
  const calls = [];
  const rpc = {
    async createChild(body) {
      calls.push(["create", body]);
      return {
        runId: `run-${body.runIndex}`,
        childSessionId: `${body.agentName}-child`,
        parentSessionId: body.parentSessionId,
        projectId: "project-1",
        parentToolCallId: body.parentToolCallId,
        runIndex: body.runIndex,
        mode: body.mode,
        agent: snapshot(body.agentName),
      };
    },
    async finalize(body) {
      calls.push(["finalize", body]);
      return {
        ok: true,
        status: "succeeded",
        runId: body.runId,
        childSessionId: body.childSessionId,
        parentSessionId: "parent-1",
        projectId: "project-1",
      };
    },
  };
  registerSubagentTool(
    { registerTool: (tool) => tools.push(tool) },
    {
      GSD_DAEMON_SOCKET: "/tmp/daemon.sock",
      GSD_PARENT_SESSION_ID: "parent-1",
      GSD_AGENT_DIR: "/tmp/agents",
      GSD_SUBAGENT_AUTH_TOKEN: "token",
    },
    {
      rpc,
      async runChildAgent({ agent }) {
        return {
          finalText: `${agent.name} done`,
          usage: { input: 2, output: 3, cost: 0.000005, turns: 1 },
        };
      },
    },
  );

  const result = await tools[0].execute("toolu_2", {
    tasks: [
      { agentName: "explorer", task: "Map files." },
      { agentName: "reviewer", task: "Review files." },
    ],
  });

  assert.equal(result.isError, false);
  assert.equal(result.details.mode, "parallel");
  assert.equal(result.details.results.length, 2);
  assert.equal(result.details.usage.input, 4);
  assert.equal(calls.filter(([kind]) => kind === "create").length, 2);
  assert.equal(calls.filter(([kind]) => kind === "finalize").length, 2);
  assert.equal(calls[0][1].parentToolCallId, "toolu_2");
  assert.equal(calls[0][1].runIndex, 1);
  assert.equal(calls[0][1].mode, "parallel");
  assert.equal(calls[1][1].parentToolCallId, "toolu_2");
  assert.equal(calls[1][1].runIndex, 2);
});

test("subagent tool chains prior results into later tasks", async () => {
  const tools = [];
  const seenTasks = [];
  const rpc = {
    async createChild(body) {
      return {
        runId: `run-${seenTasks.length + 1}`,
        childSessionId: `${body.agentName}-${seenTasks.length + 1}`,
        parentSessionId: body.parentSessionId,
        projectId: "project-1",
        parentToolCallId: body.parentToolCallId,
        runIndex: body.runIndex,
        mode: body.mode,
        agent: snapshot(body.agentName),
      };
    },
    async finalize(body) {
      return {
        ok: true,
        status: "succeeded",
        runId: body.runId,
        childSessionId: body.childSessionId,
        parentSessionId: "parent-1",
        projectId: "project-1",
      };
    },
  };
  registerSubagentTool(
    { registerTool: (tool) => tools.push(tool) },
    {
      GSD_DAEMON_SOCKET: "/tmp/daemon.sock",
      GSD_PARENT_SESSION_ID: "parent-1",
      GSD_AGENT_DIR: "/tmp/agents",
      GSD_SUBAGENT_AUTH_TOKEN: "token",
    },
    {
      rpc,
      async runChildAgent({ agent, task }) {
        seenTasks.push(task);
        return {
          finalText: `${agent.name} result`,
          usage: { input: 1, output: 1, cost: 0, turns: 1 },
        };
      },
    },
  );

  const result = await tools[0].execute("toolu_3", {
    chain: [
      { agentName: "explorer", task: "Map files." },
      { agentName: "reviewer", task: "Review files.", previous: true },
    ],
  });

  assert.equal(result.isError, false);
  assert.equal(result.details.mode, "chain");
  assert.equal(seenTasks.length, 2);
  assert.match(seenTasks[1], /explorer result/);
});

test("subagent tool finalizes cancelled child runs as cancelled", async () => {
  const tools = [];
  const finalized = [];
  const signal = AbortSignal.abort();
  const rpc = {
    async createChild(body) {
      return {
        runId: "run-cancelled",
        childSessionId: "child-cancelled",
        parentSessionId: body.parentSessionId,
        projectId: "project-1",
        parentToolCallId: body.parentToolCallId,
        runIndex: body.runIndex,
        mode: body.mode,
        agent: snapshot(body.agentName),
      };
    },
    async finalize(body) {
      finalized.push(body);
      return {
        ok: true,
        status: body.status,
        runId: body.runId,
        childSessionId: body.childSessionId,
        parentSessionId: "parent-1",
        projectId: "project-1",
      };
    },
  };
  registerSubagentTool(
    { registerTool: (tool) => tools.push(tool) },
    {
      GSD_DAEMON_SOCKET: "/tmp/daemon.sock",
      GSD_PARENT_SESSION_ID: "parent-1",
      GSD_AGENT_DIR: "/tmp/agents",
      GSD_SUBAGENT_AUTH_TOKEN: "token",
    },
    {
      rpc,
      async runChildAgent() {
        throw new Error("subagent cancelled");
      },
    },
  );

  const result = await tools[0].execute(
    "toolu_4",
    {
      agentName: "explorer",
      task: "Map files.",
    },
    signal,
  );

  assert.equal(result.isError, true);
  assert.equal(result.content[0].text, "subagent cancelled");
  assert.equal(result.details.status, "cancelled");
  assert.equal(finalized[0].status, "cancelled");
  assert.equal(finalized[0].runId, "run-cancelled");
});

test("subagent tool keeps successful finalization independent from late aborts", async () => {
  const tools = [];
  const controller = new AbortController();
  const rpc = {
    async createChild(body) {
      return {
        runId: "run-late-abort",
        childSessionId: "child-late-abort",
        parentSessionId: body.parentSessionId,
        projectId: "project-1",
        parentToolCallId: body.parentToolCallId,
        runIndex: body.runIndex,
        mode: body.mode,
        agent: snapshot(body.agentName),
      };
    },
    async finalize(body, signal) {
      assert.equal(signal, undefined);
      return {
        ok: true,
        status: "succeeded",
        runId: body.runId,
        childSessionId: body.childSessionId,
        parentSessionId: "parent-1",
        projectId: "project-1",
      };
    },
  };
  registerSubagentTool(
    { registerTool: (tool) => tools.push(tool) },
    {
      GSD_DAEMON_SOCKET: "/tmp/daemon.sock",
      GSD_PARENT_SESSION_ID: "parent-1",
      GSD_AGENT_DIR: "/tmp/agents",
      GSD_SUBAGENT_AUTH_TOKEN: "token",
    },
    {
      rpc,
      async runChildAgent() {
        controller.abort();
        return {
          finalText: "Finished before cancellation arrived.",
          usage: { input: 3, output: 4, cost: 0.000007, turns: 1 },
        };
      },
    },
  );

  const result = await tools[0].execute(
    "toolu_late_abort",
    {
      agentName: "explorer",
      task: "Map files.",
    },
    controller.signal,
  );

  assert.equal(result.isError, false);
  assert.equal(result.details.status, "succeeded");
});

test("runChildAgent sends the child task as an RPC prompt frame", async () => {
  const tempDir = await mkdtemp(join(tmpdir(), "gsd-subagent-prompt-"));
  const script = join(tempDir, "fake-pi.mjs");
  const binary =
    process.platform === "win32" ? join(tempDir, "fake-pi.cmd") : script;
  const recordPath = join(tempDir, "record.json");
  const previousBinary = process.env.GSD_PI_BINARY;

  await writeFile(
    script,
    `#!/usr/bin/env node
import { writeFileSync } from "node:fs";

let stdin = "";
let emitted = false;

function emit() {
  if (emitted) return;
  emitted = true;
  writeFileSync(${JSON.stringify(recordPath)}, JSON.stringify({
    argv: process.argv.slice(2),
    stdin,
  }));
  console.log(JSON.stringify({
    type: "agent_end",
    messages: [{
      role: "assistant",
      content: [{ type: "text", text: "done" }],
      usage: { input: 2, output: 3, cost: { total: 0.000005 } },
    }],
  }));
}

process.stdin.on("data", (chunk) => {
  stdin += chunk.toString();
});
process.stdin.on("end", emit);
setTimeout(emit, 250);
`,
  );
  if (process.platform === "win32") {
    await writeFile(binary, '@echo off\r\nnode "%~dp0fake-pi.mjs" %*\r\n');
  } else {
    await chmod(binary, 0o755);
  }

  process.env.GSD_PI_BINARY = binary;

  try {
    const forwarded = [];
    const result = await runChildAgent({
      agent: snapshot("explorer", { tools: ["read", "search"] }),
      task: "Map the subagent rendering files.",
      childSessionId: "child-1",
      runId: "run-1",
      rpc: {
        async registerProcess(body) {
          assert.equal(body.runId, "run-1");
          assert.equal(body.childSessionId, "child-1");
          assert.equal(typeof body.pid, "number");
        },
        async heartbeat(body) {
          assert.equal(body.status, "running");
        },
        async forwardEvent(body) {
          forwarded.push(body.event.type);
        },
      },
    });

    const record = JSON.parse(await readFile(recordPath, "utf8"));
    const modeIndex = record.argv.indexOf("--mode");
    assert.equal(record.argv[modeIndex + 1], "rpc");
    assert.equal(
      record.argv.includes("Map the subagent rendering files."),
      false,
    );
    assert.deepEqual(JSON.parse(record.stdin.trim()), {
      id: "subagent-task-prompt",
      type: "prompt",
      message: "Map the subagent rendering files.",
    });
    assert.equal(result.finalText, "done");
    assert.deepEqual(forwarded, ["agent_end"]);
  } finally {
    if (previousBinary === undefined) delete process.env.GSD_PI_BINARY;
    else process.env.GSD_PI_BINARY = previousBinary;
    await rm(tempDir, { recursive: true, force: true });
  }
});

test("runChildAgent excludes unrelated host secrets from child env", async () => {
  const tempDir = await mkdtemp(join(tmpdir(), "gsd-subagent-env-"));
  const script = join(tempDir, "fake-pi.mjs");
  const binary =
    process.platform === "win32" ? join(tempDir, "fake-pi.cmd") : script;
  const recordPath = join(tempDir, "record.json");
  const previous = {
    GSD_PI_BINARY: process.env.GSD_PI_BINARY,
    OPENROUTER_API_KEY: process.env.OPENROUTER_API_KEY,
    OPENAI_API_KEY: process.env.OPENAI_API_KEY,
    ANTHROPIC_API_KEY: process.env.ANTHROPIC_API_KEY,
    AWS_SECRET_ACCESS_KEY: process.env.AWS_SECRET_ACCESS_KEY,
    SECRET_TOKEN: process.env.SECRET_TOKEN,
    GSD_PARENT_SESSION_ID: process.env.GSD_PARENT_SESSION_ID,
    GSD_AGENT_DIR: process.env.GSD_AGENT_DIR,
    GSD_DAEMON_SOCKET: process.env.GSD_DAEMON_SOCKET,
    GSD_SUBAGENT_AUTH_TOKEN: process.env.GSD_SUBAGENT_AUTH_TOKEN,
  };

  await writeFile(
    script,
    `#!/usr/bin/env node
import { writeFileSync } from "node:fs";
writeFileSync(${JSON.stringify(recordPath)}, JSON.stringify(process.env));
process.stdin.resume();
process.stdin.on("end", () => {
  console.log(JSON.stringify({
    type: "agent_end",
    messages: [{
      role: "assistant",
      content: [{ type: "text", text: "done" }],
      usage: { input: 1, output: 1, cost: { total: 0 } },
    }],
  }));
});
`,
  );
  if (process.platform === "win32") {
    await writeFile(binary, '@echo off\r\nnode "%~dp0fake-pi.mjs" %*\r\n');
  } else {
    await chmod(binary, 0o755);
  }

  Object.assign(process.env, {
    GSD_PI_BINARY: binary,
    OPENROUTER_API_KEY: "sk-or-selected",
    OPENAI_API_KEY: "sk-openai",
    ANTHROPIC_API_KEY: "sk-anthropic",
    AWS_SECRET_ACCESS_KEY: "aws-secret",
    SECRET_TOKEN: "arbitrary-secret",
    GSD_PARENT_SESSION_ID: "parent-1",
    GSD_AGENT_DIR: "/tmp/agents",
    GSD_DAEMON_SOCKET: "/tmp/daemon.sock",
    GSD_SUBAGENT_AUTH_TOKEN: "subagent-token",
  });

  try {
    await runChildAgent({
      agent: snapshot("explorer", { model: "z-ai/glm-4.7-flash" }),
      task: "Map files.",
      childSessionId: "child-1",
      runId: "run-1",
      rpc: {
        async registerProcess() {},
        async heartbeat() {},
        async forwardEvent() {},
      },
    });

    const env = JSON.parse(await readFile(recordPath, "utf8"));
    assert.equal(env.OPENROUTER_API_KEY, "sk-or-selected");
    assert.equal(env.GSD_PARENT_SESSION_ID, "child-1");
    assert.equal(env.GSD_AGENT_DIR, "/tmp/agents");
    assert.equal(env.GSD_DAEMON_SOCKET, "/tmp/daemon.sock");
    assert.equal(env.GSD_SUBAGENT_AUTH_TOKEN, "subagent-token");
    assert.equal(env.OPENAI_API_KEY, undefined);
    assert.equal(env.ANTHROPIC_API_KEY, undefined);
    assert.equal(env.AWS_SECRET_ACCESS_KEY, undefined);
    assert.equal(env.SECRET_TOKEN, undefined);
  } finally {
    for (const [key, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
    await rm(tempDir, { recursive: true, force: true });
  }
});

test("runChildAgent fails when the child exits without agent_end", async () => {
  const tempDir = await mkdtemp(join(tmpdir(), "gsd-subagent-no-end-"));
  const script = join(tempDir, "fake-pi.mjs");
  const binary =
    process.platform === "win32" ? join(tempDir, "fake-pi.cmd") : script;
  const previousBinary = process.env.GSD_PI_BINARY;

  await writeFile(
    script,
    `#!/usr/bin/env node
process.stdin.resume();
process.stdin.on("end", () => process.exit(0));
`,
  );
  if (process.platform === "win32") {
    await writeFile(binary, '@echo off\r\nnode "%~dp0fake-pi.mjs" %*\r\n');
  } else {
    await chmod(binary, 0o755);
  }

  process.env.GSD_PI_BINARY = binary;

  try {
    await assert.rejects(
      runChildAgent({
        agent: snapshot("explorer"),
        task: "Map files.",
        childSessionId: "child-1",
        runId: "run-1",
        rpc: {
          async registerProcess() {},
          async heartbeat() {},
          async forwardEvent() {},
        },
      }),
      /without emitting agent_end/,
    );
  } finally {
    if (previousBinary === undefined) delete process.env.GSD_PI_BINARY;
    else process.env.GSD_PI_BINARY = previousBinary;
    await rm(tempDir, { recursive: true, force: true });
  }
});

test("subagent tool policy parses and checks categories", () => {
  const allowed = parseAllowedTools("read,search");
  assert.deepEqual([...allowed], ["read", "search"]);
  assert.equal(isToolAllowed("shell", allowed), false);
  assert.equal(isToolAllowed("read", allowed), true);
});
