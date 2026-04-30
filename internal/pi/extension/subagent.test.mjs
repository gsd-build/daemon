import test from "node:test";
import assert from "node:assert/strict";
import { registerSubagentTool } from "./subagent.js";

test("registerSubagentTool is gated by daemon subagent env", () => {
  const tools = [];
  const registered = registerSubagentTool({ registerTool: (tool) => tools.push(tool) }, {}, {});
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
        childSessionId: "child-1",
        parentSessionId: body.parentSessionId,
        projectId: "project-1",
      };
    },
    async finalize(body) {
      calls.push(["finalize", body]);
      return {
        ok: true,
        status: body.status,
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
    },
    {
      rpc,
      async findAgent(name) {
        return {
          name,
          model: "claude-haiku-4-5-20251001",
          systemPrompt: "Inspect files.",
          tools: ["read"],
        };
      },
      async runChildAgent() {
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
        agentName: "explorer",
        task: "Map the flow.",
      },
    ],
    [
      "finalize",
      {
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

test("subagent tool runs parallel child tasks and aggregates details", async () => {
  const tools = [];
  const calls = [];
  const rpc = {
    async createChild(body) {
      calls.push(["create", body]);
      return {
        childSessionId: `${body.agentName}-child`,
        parentSessionId: body.parentSessionId,
        projectId: "project-1",
      };
    },
    async finalize(body) {
      calls.push(["finalize", body]);
      return {
        ok: true,
        status: body.status,
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
    },
    {
      rpc,
      async findAgent(name) {
        return {
          name,
          model: "claude-haiku-4-5-20251001",
          systemPrompt: "Inspect files.",
          tools: ["read"],
        };
      },
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
});

test("subagent tool chains prior results into later tasks", async () => {
  const tools = [];
  const seenTasks = [];
  const rpc = {
    async createChild(body) {
      return {
        childSessionId: `${body.agentName}-${seenTasks.length + 1}`,
        parentSessionId: body.parentSessionId,
        projectId: "project-1",
      };
    },
    async finalize(body) {
      return {
        ok: true,
        status: body.status,
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
    },
    {
      rpc,
      async findAgent(name) {
        return {
          name,
          model: "claude-haiku-4-5-20251001",
          systemPrompt: "Inspect files.",
          tools: ["read"],
        };
      },
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
        childSessionId: "child-cancelled",
        parentSessionId: body.parentSessionId,
        projectId: "project-1",
      };
    },
    async finalize(body) {
      finalized.push(body);
      return {
        ok: true,
        status: body.status,
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
    },
    {
      rpc,
      async findAgent(name) {
        return {
          name,
          model: "claude-haiku-4-5-20251001",
          systemPrompt: "Inspect files.",
          tools: ["read"],
        };
      },
      async runChildAgent() {
        throw new Error("subagent cancelled");
      },
    },
  );

  const result = await tools[0].execute("toolu_4", {
    agentName: "explorer",
    task: "Map files.",
  }, signal);

  assert.equal(result.isError, true);
  assert.equal(result.details.status, "cancelled");
  assert.equal(finalized[0].status, "cancelled");
});
