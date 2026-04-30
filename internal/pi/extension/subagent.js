import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";
import { Type } from "@sinclair/typebox";
import { createDaemonRpc } from "./daemon-rpc.js";

const MAX_PARALLEL = 4;

function textFromAgentEnd(event) {
  const messages = Array.isArray(event?.messages) ? event.messages : [];
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i];
    if (message?.role !== "assistant" || !Array.isArray(message.content)) continue;
    const parts = message.content
      .filter((item) => item?.type === "text" && typeof item.text === "string")
      .map((item) => item.text);
    if (parts.length > 0) return parts.join("");
  }
  return "";
}

function usageFromAgentEnd(event) {
  const usage = { input: 0, output: 0, cost: 0, turns: 0 };
  const messages = Array.isArray(event?.messages) ? event.messages : [];
  for (const message of messages) {
    if (message?.role === "assistant") usage.turns += 1;
    const u = message?.usage;
    if (!u) continue;
    usage.input += Number(u.input ?? u.input_tokens ?? 0);
    usage.output += Number(u.output ?? u.output_tokens ?? 0);
    usage.cost += Number(u.cost?.total ?? 0);
  }
  return usage;
}

function buildAgentSystemPrompt(agent) {
  const tools = Array.isArray(agent.tools) && agent.tools.length > 0
    ? agent.tools.join(", ")
    : "default";
  return `${agent.systemPrompt}\n\nConfigured tool scope: ${tools}.`;
}

function cancelledError(message = "subagent cancelled") {
  const err = new Error(message);
  err.cancelled = true;
  return err;
}

function isCancelledError(err) {
  return Boolean(err && typeof err === "object" && err.cancelled === true);
}

function childStatusFromError(err, signal) {
  return signal?.aborted || isCancelledError(err) ? "cancelled" : "failed";
}

export async function runChildAgent({ agent, task, childSessionId, runId, rpc, signal }) {
  const extensionPath = fileURLToPath(new URL("./index.ts", import.meta.url));
  const binary = process.env.GSD_PI_BINARY || "pi";
  const args = [
    "-p",
    "--mode",
    "json",
    "-e",
    extensionPath,
    "--provider",
    "claude-cli",
    "--no-extensions",
    "--no-prompt-templates",
    "--offline",
    "--no-session",
    "--model",
    agent.model,
    "--append-system-prompt",
    buildAgentSystemPrompt(agent),
    task,
  ];
  const child = spawn(binary, args, {
    cwd: process.cwd(),
    env: {
      ...process.env,
      GSD_PARENT_SESSION_ID: childSessionId,
      GSD_AGENT_DIR: process.env.GSD_AGENT_DIR || "",
      GSD_DAEMON_SOCKET: process.env.GSD_DAEMON_SOCKET || "",
      GSD_SUBAGENT_ALLOWED_TOOLS: Array.isArray(agent.tools) ? agent.tools.join(",") : "",
    },
    stdio: ["ignore", "pipe", "pipe"],
    detached: process.platform !== "win32",
  });

  let finalText = "";
  let usage = { input: 0, output: 0, cost: 0, turns: 0 };
  const stderr = [];
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk) => stderr.push(chunk));
  let killTimer = null;
  const killChild = () => {
    if (!child.pid || child.exitCode !== null || child.killed) return;
    try {
      if (process.platform === "win32") child.kill("SIGTERM");
      else process.kill(-child.pid, "SIGTERM");
    } catch {
      child.kill("SIGTERM");
    }
    killTimer = setTimeout(() => {
      if (!child.pid || child.exitCode !== null) return;
      try {
        if (process.platform === "win32") child.kill("SIGKILL");
        else process.kill(-child.pid, "SIGKILL");
      } catch {
        child.kill("SIGKILL");
      }
    }, 2_000);
  };
  if (signal?.aborted) killChild();
  else signal?.addEventListener("abort", killChild, { once: true });
  const closePromise = new Promise((resolve, reject) => {
    child.on("error", reject);
    child.on("close", (code, signalName) => {
      signal?.removeEventListener("abort", killChild);
      if (killTimer) clearTimeout(killTimer);
      resolve({ code, signalName });
    });
  });
  if (child.pid && typeof rpc.registerProcess === "function") {
    try {
      await rpc.registerProcess({ runId, childSessionId, pid: child.pid }, signal);
      if (typeof rpc.heartbeat === "function") {
        await rpc.heartbeat({ runId, childSessionId, pid: child.pid, status: "running" }, signal);
      }
    } catch (err) {
      killChild();
      try {
        await closePromise;
      } catch {
        // Preserve the daemon RPC failure as the reason this child failed.
      }
      throw err;
    }
  }
  const lines = createInterface({ input: child.stdout });
  for await (const line of lines) {
    if (!line.trim()) continue;
    let event;
    try {
      event = JSON.parse(line);
    } catch {
      continue;
    }
    try {
      await rpc.forwardEvent({ runId, childSessionId, event }, signal);
    } catch (err) {
      killChild();
      try {
        await closePromise;
      } catch {
        // Preserve the daemon RPC failure as the reason this child failed.
      }
      throw err;
    }
    if (event?.type === "agent_end") {
      finalText = textFromAgentEnd(event);
      usage = usageFromAgentEnd(event);
    }
  }
  const { code, signalName } = await closePromise;
  if (signal?.aborted || signalName === "SIGTERM" || signalName === "SIGKILL") {
    throw cancelledError();
  }
  if (code !== 0) {
    throw new Error(stderr.join("").trim() || `subagent exited with code ${code}`);
  }
  return { finalText, usage };
}

function taskAgentName(task) {
  return task?.agentName ?? task?.agent;
}

function normalizeSingle(params) {
  const agentName = taskAgentName(params);
  if (!agentName || typeof params?.task !== "string") return null;
  return { agentName, task: params.task };
}

function runDescriptor({ toolCallId, index, mode, task }) {
  return {
    parentToolCallId: toolCallId,
    runIndex: index,
    mode,
    agentName: task.agentName,
    task: task.task,
  };
}

function isSuccessfulStatus(status) {
  return status === "done" || status === "succeeded";
}

function sumUsage(results) {
  return results.reduce(
    (total, result) => ({
      input: total.input + Number(result.usage?.input ?? 0),
      output: total.output + Number(result.usage?.output ?? 0),
      cost: total.cost + Number(result.usage?.cost ?? 0),
      turns: total.turns + Number(result.usage?.turns ?? 0),
    }),
    { input: 0, output: 0, cost: 0, turns: 0 },
  );
}

function summarizeResults(results) {
  return results
    .map((result, index) => {
      const name = result.agentName ?? `step-${index + 1}`;
      const text = result.finalText ?? result.errorMessage ?? "";
      return `${name}: ${text}`.trim();
    })
    .filter(Boolean)
    .join("\n");
}

async function runOne({ env, deps, rpc, toolCallId, task, runIndex = 1, mode = "single", signal }) {
  const descriptor = runDescriptor({ toolCallId, index: runIndex, mode, task });
  const created = await rpc.createChild({
    parentSessionId: env.GSD_PARENT_SESSION_ID,
    parentToolCallId: descriptor.parentToolCallId,
    runIndex: descriptor.runIndex,
    mode: descriptor.mode,
    agentName: descriptor.agentName,
    task: descriptor.task,
  }, signal);
  const agent = created.agent ?? {
    name: descriptor.agentName,
    model: created.model ?? "",
    systemPrompt: "",
    tools: [],
  };

  try {
    const run = await (deps.runChildAgent ?? runChildAgent)({
      agent,
      task: descriptor.task,
      childSessionId: created.childSessionId,
      runId: created.runId,
      rpc,
      signal,
    });
    const finalized = await rpc.finalize({
      runId: created.runId,
      childSessionId: created.childSessionId,
      status: "done",
      totalInputTokens: run.usage.input,
      totalOutputTokens: run.usage.output,
      totalCostUsd: String(run.usage.cost || 0),
      turnCount: Math.max(1, run.usage.turns || 1),
      finalText: run.finalText,
    }, signal);
    return {
      status: finalized.status ?? "succeeded",
      runId: created.runId,
      childSessionId: created.childSessionId,
      parentSessionId: created.parentSessionId,
      projectId: created.projectId,
      parentToolCallId: descriptor.parentToolCallId,
      runIndex: descriptor.runIndex,
      mode: descriptor.mode,
      agentName: agent.name,
      model: agent.model,
      task: descriptor.task,
      finalText: finalized.finalText ?? run.finalText,
      usage: run.usage,
      ...finalized,
    };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    const status = childStatusFromError(err, signal);
    const finalized = await rpc.finalize({
      runId: created.runId,
      childSessionId: created.childSessionId,
      status,
      totalInputTokens: 0,
      totalOutputTokens: 0,
      totalCostUsd: "0",
      turnCount: 1,
      errorMessage: message,
    }).catch(() => ({}));
    return {
      status,
      runId: created.runId,
      childSessionId: created.childSessionId,
      parentSessionId: created.parentSessionId,
      projectId: created.projectId,
      parentToolCallId: descriptor.parentToolCallId,
      runIndex: descriptor.runIndex,
      mode: descriptor.mode,
      agentName: agent.name,
      model: agent.model,
      task: descriptor.task,
      errorMessage: message,
      usage: { input: 0, output: 0, cost: 0, turns: 1 },
      ...finalized,
    };
  }
}

async function runParallel({ env, deps, rpc, toolCallId, tasks, signal }) {
  const results = await Promise.all(
    tasks.map((task, index) =>
      runOne({
        env,
        deps,
        rpc,
        toolCallId,
        task,
        runIndex: index + 1,
        mode: "parallel",
        signal,
      }),
    ),
  );
  return { mode: "parallel", results, usage: sumUsage(results) };
}

async function runChain({ env, deps, rpc, toolCallId, chain, signal }) {
  const results = [];
  for (const [index, task] of chain.entries()) {
    const previousText = summarizeResults(results);
    const taskText = task.previous && previousText
      ? `${task.task}\n\nPrior subagent results:\n${previousText}`
      : task.task;
    const result = await runOne({
      env,
      deps,
      rpc,
      toolCallId,
      task: { ...task, task: taskText },
      runIndex: index + 1,
      mode: "chain",
      signal,
    });
    results.push(result);
    if (!isSuccessfulStatus(result.status)) break;
  }
  return { mode: "chain", results, usage: sumUsage(results) };
}

export function registerSubagentTool(pi, env = process.env, deps = {}) {
  if (!env.GSD_DAEMON_SOCKET || !env.GSD_PARENT_SESSION_ID || !env.GSD_AGENT_DIR) {
    return false;
  }
  const rpc = deps.rpc ?? createDaemonRpc(env.GSD_DAEMON_SOCKET);
  if (!rpc) return false;

  pi.registerTool({
    name: "subagent",
    label: "Subagent",
    description: "Delegate bounded work to configured project subagents.",
    parameters: Type.Object({
      agentName: Type.Optional(Type.String({ description: "Name of the configured subagent." })),
      agent: Type.Optional(Type.String({ description: "Name of the configured subagent." })),
      task: Type.Optional(Type.String({ description: "Bounded task for the subagent." })),
      tasks: Type.Optional(Type.Array(Type.Object({
        agentName: Type.Optional(Type.String()),
        agent: Type.Optional(Type.String()),
        task: Type.String(),
      }))),
      chain: Type.Optional(Type.Array(Type.Object({
        agentName: Type.Optional(Type.String()),
        agent: Type.Optional(Type.String()),
        task: Type.String(),
        previous: Type.Optional(Type.Boolean()),
      }))),
    }),
    async execute(toolCallId, params, signal) {
      const single = normalizeSingle(params);
      const parallelTasks = Array.isArray(params?.tasks)
        ? params.tasks.map((task) => ({ agentName: taskAgentName(task), task: task.task }))
        : [];
      const chainTasks = Array.isArray(params?.chain)
        ? params.chain.map((task) => ({ agentName: taskAgentName(task), task: task.task, previous: task.previous === true }))
        : [];
      const modeCount = (single ? 1 : 0) + (parallelTasks.length > 0 ? 1 : 0) + (chainTasks.length > 0 ? 1 : 0);
      if (modeCount !== 1) {
        return {
          content: [{ type: "text", text: "Provide exactly one of: agentName+task, tasks, chain." }],
          isError: true,
          details: { status: "failed" },
        };
      }
      if (parallelTasks.length > MAX_PARALLEL) {
        return {
          content: [{ type: "text", text: `Too many parallel subagents. Maximum is ${MAX_PARALLEL}.` }],
          isError: true,
          details: { status: "failed", maxParallel: MAX_PARALLEL },
        };
      }
      try {
        const details = single
          ? await runOne({ env, deps, rpc, toolCallId, task: single, signal })
          : parallelTasks.length > 0
            ? await runParallel({ env, deps, rpc, toolCallId, tasks: parallelTasks, signal })
            : await runChain({ env, deps, rpc, toolCallId, chain: chainTasks, signal });
        const isGrouped = Array.isArray(details.results);
        const isError = isGrouped
          ? details.results.some((result) => !isSuccessfulStatus(result.status))
          : !isSuccessfulStatus(details.status);
        const finalText = isGrouped ? summarizeResults(details.results) : details.finalText;
        return {
          content: [{ type: "text", text: finalText || "Subagent completed." }],
          isError,
          details,
        };
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        return {
          content: [{ type: "text", text: message }],
          isError: true,
          details: {
            status: childStatusFromError(err, signal),
            errorMessage: message,
            available: Array.isArray(err?.available) ? err.available : undefined,
          },
        };
      }
    },
  });
  return true;
}
