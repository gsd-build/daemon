import { spawn, type ChildProcess } from "node:child_process";
import { createHash } from "node:crypto";
import { createInterface, type Interface } from "node:readline";
import {
  createAssistantMessageEventStream,
  type AssistantMessage,
  type AssistantMessageEventStream,
  type Context,
  type Message,
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
  nativeTools: Map<string, NativeToolCall[]>;
};

type PendingBridge = {
  codexRequestId: string | number;
  toolCallId: string;
};

type NativeToolCall = {
  itemId: string;
  toolCallId: string;
  toolName: string;
  args: Record<string, unknown>;
  idx: number;
};

const CODEX_BIN = process.env.CODEX_BIN || "codex";
const REQUEST_TIMEOUT_MS = 90_000;
const DEFAULT_CODEX_SANDBOX = "workspace-write";
const DEFAULT_CODEX_APPROVAL_POLICY = "on-request";
const FULL_ACCESS_GRANT_ENV = "GSD_CODEX_FULL_ACCESS_GRANT";
const FULL_ACCESS_GRANT_VALUE = "allow-danger-full-access";

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
  {
    id: "gpt-5.4-mini",
    name: "GPT-5.4 Mini",
    api: "openai-responses" as any,
    provider: "codex-appserver",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 400_000,
    maxTokens: 128_000,
  },
  {
    id: "gpt-5.3-codex",
    name: "GPT-5.3 Codex",
    api: "openai-responses" as any,
    provider: "codex-appserver",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 400_000,
    maxTokens: 128_000,
  },
  {
    id: "gpt-5.3-codex-spark",
    name: "GPT-5.3 Codex Spark",
    api: "openai-responses" as any,
    provider: "codex-appserver",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 400_000,
    maxTokens: 128_000,
  },
  {
    id: "gpt-5.2",
    name: "GPT-5.2",
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

function numberField(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0;
}

function firstNumberField(record: Record<string, unknown>, fields: string[]) {
  for (const field of fields) {
    const value = numberField(record[field]);
    if (value > 0) return value;
  }
  return 0;
}

export function codexUsageFromTokenUsage(tokenUsage: unknown) {
  if (!isRecord(tokenUsage)) return null;
  const total = isRecord(tokenUsage.total) ? tokenUsage.total : tokenUsage;
  const input = firstNumberField(total, ["inputTokens", "input_tokens", "input"]);
  const output = firstNumberField(total, ["outputTokens", "output_tokens", "output"]);
  const cacheRead = firstNumberField(total, [
    "cacheReadInputTokens",
    "cachedInputTokens",
    "cache_read_input_tokens",
    "cacheRead",
  ]);
  const cacheWrite = firstNumberField(total, [
    "cacheCreationInputTokens",
    "cacheWriteInputTokens",
    "cache_creation_input_tokens",
    "cacheWrite",
  ]);
  const reportedTotal = firstNumberField(total, ["totalTokens", "total_tokens", "total"]);
  return {
    input,
    output,
    cacheRead,
    cacheWrite,
    totalTokens: reportedTotal || input + output + cacheRead + cacheWrite,
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

function assistantMessageHasPayload(message: AssistantMessage) {
  return message.content.some((block: any) => {
    if (block?.type === "text") return typeof block.text === "string" && block.text.trim().length > 0;
    if (block?.type === "toolCall") return typeof block.name === "string" && block.name.length > 0;
    return Boolean(block);
  });
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function textFromPiContent(content: unknown) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .flatMap((block: any) => {
      if (block?.type === "text") return [block.text ?? ""];
      if (block?.type === "image") return [`[image: ${block.mimeType ?? "unknown"}]`];
      return [];
    })
    .join("");
}

function formatPiMessageForCodex(message: Message) {
  if (message.role === "user") {
    return `User:\n${textFromPiContent(message.content)}`;
  }

  if (message.role === "assistant") {
    const lines: string[] = [];
    const content = Array.isArray(message.content) ? message.content : [];
    for (const block of content) {
      if (block?.type === "text" && String(block.text ?? "").trim()) {
        lines.push(`Assistant:\n${block.text ?? ""}`);
      }
      if (block?.type === "image") {
        lines.push(`Assistant:\n[image: ${block.mimeType ?? "unknown"}]`);
      }
      if (block?.type === "toolCall") {
        const args = isRecord(block.arguments) ? block.arguments : {};
        lines.push(`Assistant tool call:\nname: ${block.name ?? "unknown"}\narguments: ${JSON.stringify(args)}`);
      }
    }
    return lines.join("\n");
  }

  if (message.role === "toolResult") {
    const status = message.isError ? "error" : "success";
    return `Tool result (${message.toolName ?? message.toolCallId ?? "unknown"} ${status}):\n${textFromPiContent(message.content)}`;
  }

  return "";
}

export function codexPromptTextFromContext(context: Pick<Context, "messages">) {
  const messages = context.messages ?? [];
  if (messages.length === 0) return "";

  const rendered = messages.map(formatPiMessageForCodex).filter((text) => text.trim().length > 0);
  if (rendered.length === 1) {
    return rendered[0]!.replace(/^User:\n/, "");
  }

  return [
    "Conversation history from the active GSD session:",
    "",
    ...rendered,
    "",
    "Continue from the latest user message.",
  ].join("\n");
}

export function codexLatestUserTextFromContext(context: Pick<Context, "messages">) {
  const latest = [...(context.messages ?? [])].reverse().find((message) => message.role === "user");
  return latest ? textFromPiContent(latest.content) : "";
}

function stringField(value: unknown) {
  return typeof value === "string" ? value : "";
}

function itemID(item: Record<string, unknown>, prefix: string, _fallbackIndex: number) {
  const id = stringField(item.id);
  const fingerprint = stableItemFingerprint(item);
  return id || (fingerprint ? `${prefix}_${fingerprint}` : `${prefix}_anonymous`);
}

function stableItemFingerprint(item: Record<string, unknown>) {
  const type = stringField(item.type);
  let payload: unknown;
  if (type === "commandExecution") {
    payload = { type, command: stringField(item.command), cwd: stringField(item.cwd) };
  } else if (type === "fileChange") {
    const firstChangePath = fileChangeSummaries(item)[0]?.path ?? "";
    payload = { type, path: stringField(item.path) || firstChangePath };
  } else if (type === "mcpToolCall") {
    payload = {
      type,
      server: stringField(item.server),
      tool: stringField(item.tool),
      arguments: isRecord(item.arguments) ? item.arguments : {},
    };
  } else if (type === "collabToolCall") {
    payload = {
      type,
      tool: stringField(item.tool),
      senderThreadId: stringField(item.senderThreadId),
      receiverThreadId: stringField(item.receiverThreadId),
      prompt: stringField(item.prompt),
    };
  } else if (type === "webSearch") {
    payload = { type, query: stringField(item.query) };
  } else if (type === "imageView") {
    payload = { type, path: stringField(item.path) };
  } else {
    return "";
  }
  return createHash("sha256").update(JSON.stringify(payload)).digest("hex").slice(0, 12);
}

function fileChangeSummaries(item: Record<string, unknown>) {
  const changes = Array.isArray(item.changes) ? item.changes : [];
  const summaries = changes
    .filter(isRecord)
    .map((change) => ({
      path: stringField(change.path),
      kind: stringField(change.kind),
    }))
    .filter((change) => change.path || change.kind);
  const path = stringField(item.path);
  return summaries.length > 0 || !path ? summaries : [{ path, kind: "" }];
}

function fileChangeDiffText(item: Record<string, unknown>) {
  const changes = Array.isArray(item.changes) ? item.changes : [];
  const diffs = changes
    .filter(isRecord)
    .map((change) => stringField(change.diff))
    .filter(Boolean)
    .join("\n");
  return diffs || stringField(item.patch);
}

function statusIsError(status: string) {
  return status === "failed" || status === "declined";
}

function codexThreadPolicy() {
  if (process.env[FULL_ACCESS_GRANT_ENV] === FULL_ACCESS_GRANT_VALUE) {
    return {
      sandbox: "danger-full-access",
      approvalPolicy: "never",
    };
  }
  return {
    sandbox: DEFAULT_CODEX_SANDBOX,
    approvalPolicy: DEFAULT_CODEX_APPROVAL_POLICY,
  };
}

function pushNativeTool(run: ActiveRun, nativeTool: NativeToolCall) {
  const calls = run.nativeTools.get(nativeTool.itemId) ?? [];
  calls.push(nativeTool);
  run.nativeTools.set(nativeTool.itemId, calls);
}

function uniqueToolCallID(run: ActiveRun, nativeTool: Omit<NativeToolCall, "idx">) {
  const occurrence = run.nativeTools.get(nativeTool.itemId)?.length ?? 0;
  return occurrence === 0 ? nativeTool.toolCallId : `${nativeTool.toolCallId}_${occurrence + 1}`;
}

function firstNativeTool(run: ActiveRun, itemId: string) {
  return run.nativeTools.get(itemId)?.[0];
}

function shiftNativeTool(run: ActiveRun, itemId: string) {
  const calls = run.nativeTools.get(itemId);
  if (!calls) return;
  calls.shift();
  if (calls.length === 0) {
    run.nativeTools.delete(itemId);
  }
}

export function codexNativeToolStartFromItem(item: unknown, fallbackIndex = 0): Omit<NativeToolCall, "idx"> | null {
  if (!isRecord(item)) return null;

  if (item.type === "commandExecution") {
    const args: Record<string, unknown> = { command: stringField(item.command) };
    const cwd = stringField(item.cwd);
    if (cwd) args.cwd = cwd;
    return {
      itemId: itemID(item, "cmd", fallbackIndex),
      toolCallId: itemID(item, "cmd", fallbackIndex),
      toolName: "shell",
      args,
    };
  }

  if (item.type === "fileChange") {
    const changes = fileChangeSummaries(item);
    return {
      itemId: itemID(item, "file", fallbackIndex),
      toolCallId: itemID(item, "file", fallbackIndex),
      toolName: "file_change",
      args: { changes },
    };
  }

  if (item.type === "mcpToolCall") {
    return {
      itemId: itemID(item, "mcp", fallbackIndex),
      toolCallId: itemID(item, "mcp", fallbackIndex),
      toolName: stringField(item.tool) || "mcp_tool",
      args: isRecord(item.arguments) ? item.arguments : {},
    };
  }

  if (item.type === "collabToolCall") {
    const args: Record<string, unknown> = {
      senderThreadId: stringField(item.senderThreadId),
    };
    const receiverThreadId = stringField(item.receiverThreadId);
    const newThreadId = stringField(item.newThreadId);
    const prompt = stringField(item.prompt);
    if (receiverThreadId) args.receiverThreadId = receiverThreadId;
    if (newThreadId) args.newThreadId = newThreadId;
    if (prompt) args.prompt = prompt;
    return {
      itemId: itemID(item, "collab", fallbackIndex),
      toolCallId: itemID(item, "collab", fallbackIndex),
      toolName: stringField(item.tool) || "collab_tool",
      args,
    };
  }

  if (item.type === "webSearch") {
    const args: Record<string, unknown> = { query: stringField(item.query) };
    if (isRecord(item.action)) args.action = item.action;
    return {
      itemId: itemID(item, "web", fallbackIndex),
      toolCallId: itemID(item, "web", fallbackIndex),
      toolName: "web_search",
      args,
    };
  }

  if (item.type === "imageView") {
    return {
      itemId: itemID(item, "image", fallbackIndex),
      toolCallId: itemID(item, "image", fallbackIndex),
      toolName: "view_image",
      args: { path: stringField(item.path) },
    };
  }

  return null;
}

export function codexNativeToolResultFromItem(item: unknown) {
  if (!isRecord(item)) return null;

  if (item.type === "commandExecution") {
    const exitCode = typeof item.exitCode === "number" ? item.exitCode : null;
    const status = stringField(item.status);
    return {
      resultText: stringField(item.aggregatedOutput),
      details: {
        exitCode,
        durationMs: typeof item.durationMs === "number" ? item.durationMs : null,
        status,
      },
      isError: statusIsError(status) || (exitCode !== null && exitCode !== 0),
    };
  }

  if (item.type === "fileChange") {
    const status = stringField(item.status);
    const changes = fileChangeSummaries(item);
    return {
      resultText: fileChangeDiffText(item),
      details: { changes, status },
      isError: statusIsError(status),
    };
  }

  if (item.type === "mcpToolCall") {
    const content = isRecord(item.result) && Array.isArray(item.result.content)
      ? item.result.content
      : [{ type: "text", text: "" }];
    return {
      resultText: content
        .filter((part: any) => part?.type === "text")
        .map((part: any) => part.text ?? "")
        .join("\n"),
      details: {
        server: stringField(item.server),
        durationMs: typeof item.durationMs === "number" ? item.durationMs : null,
        status: stringField(item.status),
      },
      isError: Boolean(item.error) || statusIsError(stringField(item.status)),
    };
  }

  if (item.type === "collabToolCall") {
    const status = stringField(item.status);
    return {
      resultText: stringField(item.agentStatus) || status,
      details: {
        status,
        newThreadId: stringField(item.newThreadId),
        receiverThreadId: stringField(item.receiverThreadId),
      },
      isError: statusIsError(status) || Boolean(item.error),
    };
  }

  if (item.type === "webSearch") {
    return {
      resultText: isRecord(item.action) ? JSON.stringify(item.action) : stringField(item.query),
      details: { query: stringField(item.query) },
      isError: false,
    };
  }

  if (item.type === "imageView") {
    return {
      resultText: stringField(item.path),
      details: { path: stringField(item.path) },
      isError: false,
    };
  }

  return null;
}

export function registerCodexAppServerProvider(pi: ExtensionAPI) {
  let codex: ChildProcess | null = null;
  let rl: Interface | null = null;
  let initialized = false;
  let initPromise: Promise<void> | null = null;
  let requestId = 0;
  let threadId: string | null = null;
  let threadHasGsdReplay = false;
  let activeTurnId: string | null = null;
  let activeRun: ActiveRun | null = null;
  let pendingBridge: PendingBridge | null = null;
  let codexStderrTail = "";
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

  function emitSyntheticToolEvent(event: any) {
    process.stdout.write(`${JSON.stringify({ ...event, _synthetic: true })}\n`);
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

  function rejectPendingRequests(error: Error) {
    for (const [id, pending] of pendingRequests) {
      clearTimeout(pending.timer);
      pending.reject(error);
      pendingRequests.delete(id);
    }
  }

  function endActiveRunWithError(message: string) {
    if (!activeRun) return;
    activeRun.output.stopReason = "error";
    activeRun.output.errorMessage = codexStderrTail ? `${message}\n\nCodex stderr:\n${codexStderrTail}` : message;
    activeRun.stream.push({ type: "error", reason: "error", error: activeRun.output });
    activeRun.stream.end();
    activeRun = null;
  }

  function rememberCodexStderr(chunk: unknown) {
    codexStderrTail = `${codexStderrTail}${String(chunk)}`.slice(-4_000);
  }

  function resetCodexState(error: Error, killProcess = false) {
    const process = codex;
    initialized = false;
    initPromise = null;
    codex = null;
    rl?.close();
    rl = null;
    threadId = null;
    threadHasGsdReplay = false;
    activeTurnId = null;
    pendingBridge = null;
    rejectPendingRequests(error);
    endActiveRunWithError(error.message);
    if (killProcess && process && !process.killed) {
      process.kill("SIGTERM");
    }
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

  function surfaceNativeToolStart(run: ActiveRun, item: unknown) {
    const start = codexNativeToolStartFromItem(item, run.output.content.length);
    if (!start) return false;

    const idx = run.output.content.length;
    const nativeTool: NativeToolCall = { ...start, toolCallId: uniqueToolCallID(run, start), idx };
    pushNativeTool(run, nativeTool);
    run.output.content.push({
      type: "toolCall",
      id: nativeTool.toolCallId,
      name: nativeTool.toolName,
      arguments: nativeTool.args,
    });

    run.stream.push({ type: "toolcall_start", contentIndex: idx, partial: run.output });
    run.stream.push({
      type: "toolcall_delta",
      contentIndex: idx,
      delta: JSON.stringify(nativeTool.args),
      partial: run.output,
    });
    emitSyntheticToolEvent({
      type: "tool_execution_start",
      toolCallId: nativeTool.toolCallId,
      toolName: nativeTool.toolName,
      args: nativeTool.args,
    });
    return true;
  }

  function surfaceNativeToolOutput(run: ActiveRun, itemId: unknown, delta: unknown) {
    const id = stringField(itemId);
    const nativeTool = id ? firstNativeTool(run, id) : undefined;
    if (!nativeTool) return false;
    const text = stringField(delta);
    if (!text) return true;

    emitSyntheticToolEvent({
      type: "tool_execution_update",
      toolCallId: nativeTool.toolCallId,
      toolName: nativeTool.toolName,
      partialResult: {
        content: [{ type: "text", text }],
        details: {},
      },
    });
    return true;
  }

  function surfaceNativeToolEnd(run: ActiveRun, item: unknown) {
    if (!isRecord(item)) return false;
    const start = codexNativeToolStartFromItem(item, run.output.content.length);
    if (!start) return false;
    const id = start.itemId;
    if (!firstNativeTool(run, id)) {
      surfaceNativeToolStart(run, item);
    }
    const nativeTool = firstNativeTool(run, id);
    if (!nativeTool) return false;
    const result = codexNativeToolResultFromItem(item);
    if (!result) return false;

    run.stream.push({
      type: "toolcall_end",
      contentIndex: nativeTool.idx,
      toolCall: {
        type: "toolCall",
        id: nativeTool.toolCallId,
        name: nativeTool.toolName,
        arguments: nativeTool.args,
      },
      partial: run.output,
    });
    emitSyntheticToolEvent({
      type: "tool_execution_end",
      toolCallId: nativeTool.toolCallId,
      toolName: nativeTool.toolName,
      result: {
        content: [{ type: "text", text: result.resultText }],
        details: result.details,
      },
      isError: result.isError,
    });
    shiftNativeTool(run, id);
    return true;
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
        } else {
          surfaceNativeToolStart(run, params.item);
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
        } else {
          surfaceNativeToolEnd(run, params.item);
        }
        break;
      case "item/commandExecution/outputDelta":
        surfaceNativeToolOutput(run, params.itemId, params.delta || params.output);
        break;
      case "item/fileChange/outputDelta":
        surfaceNativeToolOutput(run, params.itemId, params.delta || params.output);
        break;
      case "thread/tokenUsage/updated": {
        const usage = codexUsageFromTokenUsage(params.tokenUsage);
        if (usage) {
          run.output.usage.input = usage.input;
          run.output.usage.output = usage.output;
          run.output.usage.cacheRead = usage.cacheRead;
          run.output.usage.cacheWrite = usage.cacheWrite;
          run.output.usage.totalTokens = usage.totalTokens;
        }
        break;
      }
      case "turn/completed":
        run.output.stopReason = params.turn?.status === "interrupted" ? "aborted" : "stop";
        if (run.output.stopReason === "stop" && !assistantMessageHasPayload(run.output)) {
          run.output.stopReason = "error";
          run.output.errorMessage = codexStderrTail
            ? `Codex AppServer completed without assistant content.\n\nCodex stderr:\n${codexStderrTail}`
            : "Codex AppServer completed without assistant content.";
          run.stream.push({ type: "error", reason: "error", error: run.output });
          run.stream.end();
          activeRun = null;
          activeTurnId = null;
          pendingBridge = null;
          break;
        }
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
      codexStderrTail = "";
      codex = spawn(CODEX_BIN, ["app-server", "--listen", "stdio://"], {
        cwd: process.cwd(),
        stdio: ["pipe", "pipe", "pipe"],
      });
      rl = createInterface({ input: codex.stdout! });
      rl.on("line", handleCodexLine);
      codex.stderr?.on("data", rememberCodexStderr);
      codex.on("close", () => {
        resetCodexState(new Error("Codex process exited"));
      });

      try {
        await codexRequest("initialize", {
          clientInfo: { name: "gsd-daemon-pi-codex-provider", version: "0.0.1" },
          capabilities: { experimentalApi: true },
        });
        codexNotify("initialized");
        const policy = codexThreadPolicy();
        const result = await codexRequest("thread/start", {
          cwd: process.cwd(),
          sandbox: policy.sandbox,
          approvalPolicy: policy.approvalPolicy,
          model: model.id,
          dynamicTools: codexDynamicToolsFromContext(context),
          ephemeral: true,
          experimentalRawEvents: false,
          persistExtendedHistory: false,
        });
        threadId = result.thread?.id;
        threadHasGsdReplay = false;
        initialized = true;
      } catch (error) {
        resetCodexState(error instanceof Error ? error : new Error(String(error)), true);
        throw error;
      }
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

  function streamCodexAppServer(model: Model<any>, context: Context, options?: SimpleStreamOptions) {
    const stream = createAssistantMessageEventStream();
    const output = codexOutputForModel(model);
    if (activeRun) {
      output.stopReason = "error";
      output.errorMessage = "Codex AppServer provider already has an active run.";
      stream.push({ type: "error", reason: "error", error: output });
      stream.end();
      return stream;
    }
    activeRun = { stream, output, textIndex: null, nativeTools: new Map() };
    const clearActiveRun = () => {
      if (activeRun?.stream === stream) {
        activeRun = null;
      }
    };
    (stream as any).on?.("end", clearActiveRun);
    (stream as any).on?.("error", clearActiveRun);
    (stream as any).on?.("close", clearActiveRun);
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
        const text = threadHasGsdReplay
          ? codexLatestUserTextFromContext(context)
          : codexPromptTextFromContext(context);
        return codexRequest("turn/start", {
          threadId,
          input: [{ type: "text", text }],
        });
      })
      .then((result) => {
        if (result?.turn) threadHasGsdReplay = true;
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
