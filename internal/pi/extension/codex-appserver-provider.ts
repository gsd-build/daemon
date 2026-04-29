import { spawn, type ChildProcess } from "node:child_process";
import { createInterface, type Interface } from "node:readline";
import {
  createAssistantMessageEventStream,
  type AssistantMessage,
  type AssistantMessageEventStream,
  type Context,
  type Model,
  type SimpleStreamOptions,
  type Tool as PiTool,
} from "@mariozechner/pi-ai";
import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";

type RpcPending = {
  resolve: (value: any) => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
};

type ActiveRun = {
  stream: AssistantMessageEventStream;
  output: AssistantMessage;
  textIndex: number | null;
};

type PendingBridge = {
  codexRequestId: string | number;
  toolCallId: string;
};

const CODEX_BIN = process.env.CODEX_BIN || "codex";
const REQUEST_TIMEOUT_MS = 90_000;

export const codexModelDefinitions = [
  {
    id: "gpt-5.5",
    name: "GPT-5.5",
    api: "openai-responses" as any,
    provider: "codex-appserver",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 400_000,
    maxTokens: 128_000,
  },
  {
    id: "gpt-5.4",
    name: "GPT-5.4",
    api: "openai-responses" as any,
    provider: "codex-appserver",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 400_000,
    maxTokens: 128_000,
  },
] as const;

function emptyUsage() {
  return {
    input: 0,
    output: 0,
    cacheRead: 0,
    cacheWrite: 0,
    totalTokens: 0,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  };
}

export function codexOutputForModel(model: Pick<Model<any>, "id" | "api">): AssistantMessage {
  return {
    role: "assistant",
    content: [],
    api: model.api,
    provider: "codex-appserver",
    model: model.id,
    usage: emptyUsage(),
    stopReason: "stop",
    timestamp: Date.now(),
  };
}

export function codexDynamicToolsFromContext(context: Pick<Context, "tools">) {
  const tools = (context.tools as PiTool[] | undefined) ?? [];
  return tools.map((tool: any) => ({
    namespace: "gsd",
    name: tool.name,
    description: tool.description || tool.label || tool.name,
    inputSchema: tool.parameters || { type: "object", additionalProperties: true },
    deferLoading: false,
    exposeToContext: true,
  }));
}

export function registerCodexAppServerProvider(pi: ExtensionAPI) {
  let codex: ChildProcess | null = null;
  let rl: Interface | null = null;
  let initialized = false;
  let initPromise: Promise<void> | null = null;
  let requestId = 0;
  let threadId: string | null = null;
  let activeTurnId: string | null = null;
  let activeRun: ActiveRun | null = null;
  let pendingBridge: PendingBridge | null = null;
  const pendingRequests = new Map<string | number, RpcPending>();

  function sendToCodex(message: any) {
    if (!codex?.stdin || codex.killed) return;
    codex.stdin.write(`${JSON.stringify(message)}\n`);
  }

  function codexRequest(method: string, params: any = {}) {
    const id = String(++requestId);
    return new Promise<any>((resolve, reject) => {
      const timer = setTimeout(() => {
        pendingRequests.delete(id);
        reject(new Error(`Codex request timed out: ${method}`));
      }, REQUEST_TIMEOUT_MS);
      pendingRequests.set(id, { resolve, reject, timer });
      sendToCodex({ id, method, params });
    });
  }

  function codexNotify(method: string, params: any = {}) {
    sendToCodex({ method, params });
  }

  function codexRespond(id: string | number, result: any) {
    sendToCodex({ id, result });
  }

  function settleCodexResponse(message: any) {
    if (!("id" in message) || !("result" in message || "error" in message)) return false;
    const pending = pendingRequests.get(message.id);
    if (!pending) return false;
    pendingRequests.delete(message.id);
    clearTimeout(pending.timer);
    if ("error" in message) {
      pending.reject(new Error(message.error?.message || "Codex request failed"));
    } else {
      pending.resolve(message.result);
    }
    return true;
  }

  function surfaceDynamicToolCall(codexRequestId: string | number, params: any) {
    const run = activeRun;
    if (!run) {
      codexRespond(codexRequestId, {
        success: false,
        contentItems: [{ type: "inputText", text: "Pi provider had no active run for this dynamic tool call." }],
      });
      return;
    }

    const args = params.arguments || {};
    const toolName = params.tool;
    const toolCallId = params.callId || `codex_dynamic_tool_${Date.now()}`;
    const idx = run.output.content.length;
    run.output.content.push({
      type: "toolCall",
      id: toolCallId,
      name: toolName,
      arguments: args,
    });
    pendingBridge = { codexRequestId, toolCallId };

    run.stream.push({ type: "toolcall_start", contentIndex: idx, partial: run.output });
    run.stream.push({
      type: "toolcall_delta",
      contentIndex: idx,
      delta: JSON.stringify(args),
      partial: run.output,
    });
    run.stream.push({
      type: "toolcall_end",
      contentIndex: idx,
      toolCall: { type: "toolCall", id: toolCallId, name: toolName, arguments: args },
      partial: run.output,
    });
    run.output.stopReason = "toolUse";
    run.stream.push({ type: "done", reason: "toolUse", message: run.output });
    run.stream.end();
    activeRun = null;
  }

  function handleCodexLine(line: string) {
    if (!line.trim()) return;

    let message: any;
    try {
      message = JSON.parse(line);
    } catch {
      return;
    }

    if (settleCodexResponse(message)) return;

    if ("id" in message && message.method === "item/tool/call") {
      surfaceDynamicToolCall(message.id, message.params || {});
      return;
    }

    if ("id" in message && message.method?.includes("requestApproval")) {
      codexRespond(message.id, { decision: "cancel" });
      return;
    }

    const method = message.method;
    const params = message.params || {};
    const run = activeRun;
    if (!run) return;

    switch (method) {
      case "turn/started":
        activeTurnId = params.turn?.id || activeTurnId;
        break;
      case "item/started":
        if (params.item?.type === "agentMessage") {
          const idx = run.output.content.length;
          run.output.content.push({ type: "text", text: "" });
          run.textIndex = idx;
          run.stream.push({ type: "text_start", contentIndex: idx, partial: run.output });
        }
        break;
      case "item/agentMessage/delta": {
        const delta = params.delta || "";
        if (run.textIndex === null) {
          run.textIndex = run.output.content.length;
          run.output.content.push({ type: "text", text: "" });
          run.stream.push({ type: "text_start", contentIndex: run.textIndex, partial: run.output });
        }
        const textBlock = run.output.content[run.textIndex] as any;
        textBlock.text += delta;
        run.stream.push({ type: "text_delta", contentIndex: run.textIndex, delta, partial: run.output });
        break;
      }
      case "item/completed":
        if (params.item?.type === "agentMessage" && run.textIndex !== null) {
          const textBlock = run.output.content[run.textIndex] as any;
          run.stream.push({
            type: "text_end",
            contentIndex: run.textIndex,
            content: textBlock.text || "",
            partial: run.output,
          });
          run.textIndex = null;
        }
        break;
      case "thread/tokenUsage/updated": {
        const total = params.tokenUsage?.total;
        if (total) {
          run.output.usage.input = total.inputTokens || 0;
          run.output.usage.output = total.outputTokens || 0;
          run.output.usage.totalTokens = total.totalTokens || 0;
        }
        break;
      }
      case "turn/completed":
        run.output.stopReason = params.turn?.status === "interrupted" ? "aborted" : "stop";
        run.stream.push({ type: "done", reason: run.output.stopReason === "stop" ? "stop" : "length", message: run.output });
        run.stream.end();
        activeRun = null;
        activeTurnId = null;
        pendingBridge = null;
        break;
    }
  }

  async function ensureCodex(model: Model<any>, context: Context) {
    if (initialized && codex && !codex.killed) return;
    if (initPromise) return initPromise;

    initPromise = (async () => {
      codex = spawn(CODEX_BIN, ["app-server", "--listen", "stdio://"], {
        cwd: process.cwd(),
        stdio: ["pipe", "pipe", "pipe"],
      });
      rl = createInterface({ input: codex.stdout! });
      rl.on("line", handleCodexLine);
      codex.stderr?.on("data", () => {});
      codex.on("close", () => {
        initialized = false;
        initPromise = null;
        codex = null;
        rl = null;
        threadId = null;
        activeTurnId = null;
        pendingBridge = null;
        if (activeRun) {
          activeRun.output.stopReason = "error";
          activeRun.output.errorMessage = "Codex AppServer exited";
          activeRun.stream.push({ type: "error", reason: "error", error: activeRun.output });
          activeRun.stream.end();
          activeRun = null;
        }
      });

      await codexRequest("initialize", {
        clientInfo: { name: "gsd-daemon-pi-codex-provider", version: "0.0.1" },
        capabilities: { experimentalApi: true },
      });
      codexNotify("initialized");
      const result = await codexRequest("thread/start", {
        cwd: process.cwd(),
        sandbox: "read-only",
        approvalPolicy: "never",
        model: model.id,
        dynamicTools: codexDynamicToolsFromContext(context),
        ephemeral: true,
        experimentalRawEvents: false,
        persistExtendedHistory: false,
      });
      threadId = result.thread?.id;
      initialized = true;
    })();

    return initPromise;
  }

  async function continuePendingCodexTurn(context: Context) {
    if (!pendingBridge) return false;
    const latestToolResult = [...context.messages]
      .reverse()
      .find((message: any) => message.role === "toolResult" && message.toolCallId === pendingBridge?.toolCallId) as any;
    if (!latestToolResult) return false;

    const text = (latestToolResult.content || [])
      .filter((part: any) => part.type === "text")
      .map((part: any) => part.text)
      .join("\n");
    codexRespond(pendingBridge.codexRequestId, {
      success: true,
      contentItems: [{ type: "inputText", text }],
    });
    return true;
  }

  function latestUserText(context: Context) {
    const latest = [...context.messages].reverse().find((message: any) => message.role === "user") as any;
    if (!latest) return "";
    if (typeof latest.content === "string") return latest.content;
    return (latest.content || [])
      .filter((part: any) => part.type === "text")
      .map((part: any) => part.text)
      .join("\n");
  }

  function streamCodexAppServer(model: Model<any>, context: Context, options?: SimpleStreamOptions) {
    const stream = createAssistantMessageEventStream();
    const output = codexOutputForModel(model);
    activeRun = { stream, output, textIndex: null };
    stream.push({ type: "start", partial: output });

    options?.signal?.addEventListener(
      "abort",
      () => {
        if (threadId && activeTurnId) {
          void codexRequest("turn/interrupt", { threadId, turnId: activeTurnId }).catch(() => {});
        }
      },
      { once: true },
    );

    ensureCodex(model, context)
      .then(() => continuePendingCodexTurn(context))
      .then((continued) => {
        if (continued) return;
        return codexRequest("turn/start", {
          threadId,
          input: [{ type: "text", text: latestUserText(context) }],
        });
      })
      .then((result) => {
        if (result?.turn?.id) activeTurnId = result.turn.id;
      })
      .catch((error) => {
        output.stopReason = "error";
        output.errorMessage = error instanceof Error ? error.message : String(error);
        stream.push({ type: "error", reason: "error", error: output });
        stream.end();
        activeRun = null;
      });

    return stream;
  }

  pi.registerProvider("codex-appserver", {
    baseUrl: "http://localhost/codex-appserver-unused",
    apiKey: "codex-appserver-local",
    api: "openai-responses" as any,
    models: codexModelDefinitions as any,
    streamSimple: streamCodexAppServer,
  });
}
