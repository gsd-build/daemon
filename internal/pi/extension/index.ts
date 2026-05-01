/**
 * Pi provider extension for the GSD daemon.
 *
 * The extension registers a Claude Agent SDK backed provider and an
 * ask_human tool. Pi owns tool execution and UI routing; the provider streams
 * Claude SDK output into pi's assistant message event stream and returns
 * tool calls to pi for execution.
 */

import crypto from "node:crypto";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import {
  createSdkMcpServer,
  query,
  tool,
  type Options as SdkOptions,
  type SDKMessage,
  type SDKUserMessage,
  type SDKUserMessageReplay,
} from "@anthropic-ai/claude-agent-sdk";
import {
  type AssistantMessage,
  type AssistantMessageEventStream,
  type Context,
  type Message,
  type Model,
  type SimpleStreamOptions,
  type Tool as PiTool,
  createAssistantMessageEventStream,
} from "@mariozechner/pi-ai";
import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { z } from "zod";
import {
  applyUsageFromSdkMessage,
  ensureNonZeroUsageForAbortedToolTurn,
} from "./usage-estimator.js";
import { schemaToZod } from "./schema-to-zod.js";
import { askUserQuestionsTool } from "./ask-user-questions.js";
import { backgroundTools } from "./background-tools.js";
import { registerPlanTools } from "./plan-tools.js";
import { registerCodexAppServerProvider } from "./codex-appserver-provider.js";
import { registerOpenRouterProvider } from "./openrouter-provider.js";
import { WarmClaudeSdkWorker } from "./claude-sdk-worker.js";
import { registerSubagentTool } from "./subagent.js";
import {
  BROWSER_TOOL_CATEGORIES,
  BROWSER_TOOL_METHODS,
  BrowserToolCategorySchema,
  BrowserToolMethodSchema,
} from "./browser-methods.js";
import {
  filterToolsByPolicy,
  hasSubagentToolPolicy,
  isMinimalToolProfile,
  isToolAllowed,
  parseAllowedTools,
  registerIfAllowed,
  toolProfile,
} from "./tool-policy.js";
import { Type } from "@sinclair/typebox";

const CLAUDE_BUILTINS = [
  "Bash", "BashOutput", "KillShell",
  "Read", "Write", "Edit", "MultiEdit", "NotebookEdit",
  "Glob", "Grep", "WebFetch", "WebSearch",
  "Task", "Agent", "TodoWrite", "ExitPlanMode", "EnterPlanMode",
  "ListMcpResourcesTool", "ReadMcpResourceTool",
  "AskUserQuestion", "PushNotification", "ScheduleWakeup", "Monitor", "Skill", "ToolSearch",
  "TeamCreate", "TeamDelete", "SendMessage",
  "TaskCreate", "TaskUpdate", "TaskList", "TaskGet", "TaskOutput", "TaskStop",
  "EnterWorktree", "ExitWorktree",
  "RemoteTrigger",
];

const MCP_PREFIX = "mcp__pi-tools__";
const SDK_SESSION_NAMESPACE = "gsd-pi-claude-sdk:v1";
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const EPIPE_GUARD_KEY = "__gsdPiClaudeSdkEpipeGuardInstalled";

let warmClaudeWorker: WarmClaudeSdkWorker | null = null;
let warmClaudeOptionsSignature = "";

type SdkPromptInputMessage = SDKUserMessage | SDKUserMessageReplay;

type ActiveToolCall = {
  idx: number;
  id: string;
  name: string;
  jsonAcc: string;
};

type BrowserGrant = {
  grantId: string;
  browserId: string;
  sessionId: string;
};

function isWriteEpipe(err: unknown) {
  return isRecord(err) && err.code === "EPIPE" && err.syscall === "write";
}

function installClaudeSdkPipeGuard() {
  const state = globalThis as any;
  if (state[EPIPE_GUARD_KEY]) return;
  state[EPIPE_GUARD_KEY] = true;
  process.on("uncaughtException", (err) => {
    if (isWriteEpipe(err)) return;
    throw err;
  });
}

installClaudeSdkPipeGuard();

const BrowserToolParams = Type.Object({
  method: BrowserToolMethodSchema,
  category: Type.Optional(BrowserToolCategorySchema),
  params: Type.Optional(Type.Record(Type.String(), Type.Any())),
});

function browserToolDefinition() {
  return {
    name: "gsd_browser",
    label: "GSD Browser",
    description:
      "Use the active task-scoped GSD shared browser session for page navigation, inspection, ref-based interaction, screenshots, network controls, auth state, traces, and artifacts. Prefer snapshot with refs before interacting. Pass bare method names such as navigate, snapshot, click_ref, visual_diff, or vault_login; do not prefix methods with browser.",
    parameters: BrowserToolParams,
    input_schema: {
      type: "object",
      additionalProperties: false,
      properties: {
        method: {
          type: "string",
          enum: BROWSER_TOOL_METHODS,
          description:
            "Bare browser operation name. Use navigate, not browser.navigate.",
        },
        category: {
          type: "string",
          enum: BROWSER_TOOL_CATEGORIES,
          description:
            "Optional method classification for UI and policy. The daemon executes method and params.",
        },
        params: { type: "object", additionalProperties: true },
      },
      required: ["method"],
    },
  };
}

export function buildClaudeCliBrowserTools(context: { browserGrant?: BrowserGrant }) {
  return context.browserGrant ? [browserToolDefinition()] : [];
}

function piToolName(toolDef: PiTool | ReturnType<typeof browserToolDefinition>) {
  return typeof toolDef.name === "string" ? toolDef.name : undefined;
}

export function mergeClaudeCliTools(contextTools: PiTool[] | undefined, browserGrant?: BrowserGrant) {
  if (isMinimalToolProfile()) return [];
  const allowed = parseAllowedTools(process.env.GSD_SUBAGENT_ALLOWED_TOOLS);
  const merged: PiTool[] = [];
  const seenNames = new Set<string>();
  const browserTools =
    !hasSubagentToolPolicy() || isToolAllowed("browser", allowed)
      ? (buildClaudeCliBrowserTools({ browserGrant }) as unknown as PiTool[])
      : [];
  const visibleContextTools = (
    filterToolsByPolicy((contextTools ?? []) as any[], allowed) as PiTool[]
  ).filter((toolDef) => browserGrant || piToolName(toolDef) !== "gsd_browser");

  for (const toolDef of [...visibleContextTools, ...browserTools]) {
    const name = piToolName(toolDef);
    if (name) {
      if (seenNames.has(name)) continue;
      seenNames.add(name);
    }
    merged.push(toolDef);
  }

  return merged;
}

type ToolRegistrationDiagnostic = {
  name: string;
  category: string;
  schemaBytes: number;
  descriptionBytes: number;
};

function jsonBytes(value: unknown) {
  try {
    return Buffer.byteLength(JSON.stringify(value ?? {}), "utf8");
  } catch {
    return 0;
  }
}

function describeRegisteredTool(category: string, definition: any): ToolRegistrationDiagnostic {
  return {
    name: String(definition?.name ?? ""),
    category,
    schemaBytes: jsonBytes(definition?.parameters ?? definition?.input_schema ?? {}),
    descriptionBytes: Buffer.byteLength(String(definition?.description ?? ""), "utf8"),
  };
}

function emitScaffoldDiagnostics(profile: string, registeredTools: ToolRegistrationDiagnostic[]) {
  const totals = registeredTools.reduce(
    (acc, toolDef) => {
      acc.schemaBytes += toolDef.schemaBytes;
      acc.descriptionBytes += toolDef.descriptionBytes;
      acc.toolNameBytes += Buffer.byteLength(toolDef.name, "utf8");
      return acc;
    },
    { schemaBytes: 0, descriptionBytes: 0, toolNameBytes: 0 },
  );
  process.stdout.write(JSON.stringify({
    type: "scaffold_diagnostics",
    source: "extension",
    phase: "registered_tools",
    taskId: process.env.GSD_TASK_ID ?? null,
    sessionId: process.env.GSD_SESSION_ID ?? null,
    channelId: process.env.GSD_CHANNEL_ID ?? null,
    toolProfile: profile,
    registeredToolCount: registeredTools.length,
    registeredTools,
    totals,
    providers: ["claude-cli", "codex-appserver", "openrouter"],
  }) + "\n");
}

function contextMessageChars(context: Context) {
  return ((context.messages as any[]) ?? []).reduce(
    (total, message) => total + Buffer.byteLength(JSON.stringify(message?.content ?? ""), "utf8"),
    0,
  );
}

function toolSchemaBytes(tools: any[]) {
  return tools.reduce(
    (total, toolDef) => total + jsonBytes(toolDef?.parameters ?? toolDef?.input_schema ?? {}),
    0,
  );
}

function emitProviderContextDiagnostics(
  provider: string,
  model: Model<any>,
  context: Context,
  mergedTools: any[],
) {
  const contextTools = (context.tools as PiTool[] | undefined) ?? [];
  process.stdout.write(JSON.stringify({
    type: "scaffold_diagnostics",
    source: "extension",
    phase: "provider_context",
    taskId: process.env.GSD_TASK_ID ?? null,
    sessionId: process.env.GSD_SESSION_ID ?? null,
    channelId: process.env.GSD_CHANNEL_ID ?? null,
    provider,
    model: model.id,
    toolProfile: toolProfile(),
    systemPromptChars: Buffer.byteLength(String(context.systemPrompt ?? ""), "utf8"),
    deliveredSystemPromptChars: isMinimalToolProfile()
      ? 0
      : Buffer.byteLength(String(context.systemPrompt ?? ""), "utf8"),
    messageCount: ((context.messages as any[]) ?? []).length,
    messageContentChars: contextMessageChars(context),
    contextToolCount: contextTools.length,
    contextToolSchemaBytes: toolSchemaBytes(contextTools as any[]),
    mergedToolCount: mergedTools.length,
    mergedToolSchemaBytes: toolSchemaBytes(mergedTools),
  }) + "\n");
}

function registerTrackedTool(
  pi: ExtensionAPI,
  registeredTools: ToolRegistrationDiagnostic[],
  category: string,
  definition: any,
) {
  pi.registerTool(definition);
  registeredTools.push(describeRegisteredTool(category, definition));
}

function registerVisibleTool(
  pi: ExtensionAPI,
  allowed: Set<string>,
  category: string,
  definition: any,
  registeredTools?: ToolRegistrationDiagnostic[],
) {
  if (!hasSubagentToolPolicy()) {
    pi.registerTool(definition);
    registeredTools?.push(describeRegisteredTool(category, definition));
    return true;
  }
  const registered = registerIfAllowed(pi, allowed, category, definition);
  if (registered) registeredTools?.push(describeRegisteredTool(category, definition));
  return registered;
}

function browserGrantFromEnv() {
	const grantId = process.env.GSD_BROWSER_GRANT_ID;
	const browserId = process.env.GSD_BROWSER_ID;
	const sessionId = process.env.GSD_BROWSER_SESSION_ID;
	if (!grantId || !browserId || !sessionId) return undefined;
	return { grantId, browserId, sessionId };
}

function warmClaudeOptionsKey(model: Model<any>, context: Context) {
  const tools = ((context.tools as PiTool[] | undefined) ?? []).map((toolDef) => toolDef.name).sort();
  return JSON.stringify({
    model: model.id,
    systemPrompt: context.systemPrompt ?? "",
    tools,
    browserGrant: browserGrantFromEnv() ?? null,
  });
}

function resetWarmClaudeWorker() {
  void warmClaudeWorker?.stop();
  warmClaudeWorker = null;
  warmClaudeOptionsSignature = "";
}

async function browserRpc(browserId: string, method: string, params: unknown, signal?: AbortSignal) {
  const socketPath = path.join(os.homedir(), ".gsd-browser", "sessions", browserId, "daemon.sock");
  const payload = Buffer.from(JSON.stringify({
    jsonrpc: "2.0",
    id: Date.now(),
    method: "cloud_tool",
    params: { method, params: params ?? {} },
  }));
  const header = Buffer.alloc(4);
  header.writeUInt32BE(payload.length, 0);

  return await new Promise<unknown>((resolve, reject) => {
    const socket = net.createConnection(socketPath);
    const chunks: Buffer[] = [];
    let expected = 0;
    let settled = false;

    const cleanup = () => {
      signal?.removeEventListener("abort", onAbort);
      socket.removeAllListeners("timeout");
    };
    const rejectOnce = (err: Error) => {
      if (settled) return;
      settled = true;
      cleanup();
      socket.destroy();
      reject(err);
    };
    const resolveOnce = (value: unknown) => {
      if (settled) return;
      settled = true;
      cleanup();
      socket.end();
      resolve(value);
    };
    const onAbort = () => rejectOnce(new Error("browser tool aborted"));

    if (signal?.aborted) {
      rejectOnce(new Error("browser tool aborted"));
      return;
    }
    signal?.addEventListener("abort", onAbort, { once: true });
    socket.setTimeout(30_000, () => rejectOnce(new Error("browser tool timed out")));
    socket.on("connect", () => {
      socket.write(Buffer.concat([header, payload]), (err) => {
        if (err) rejectOnce(err);
      });
    });
    socket.on("data", (chunk) => {
      chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
      const all = Buffer.concat(chunks);
      if (expected === 0 && all.length >= 4) expected = all.readUInt32BE(0);
      if (expected > 16 * 1024 * 1024) {
        rejectOnce(new Error(`browser rpc frame too large: ${expected}`));
        return;
      }
      if (expected > 0 && all.length >= expected + 4) {
        try {
          const response = JSON.parse(all.subarray(4, expected + 4).toString("utf8"));
          if (response.error) rejectOnce(new Error(response.error.message ?? "browser tool failed"));
          else resolveOnce(response.result ?? {});
        } catch (err) {
          rejectOnce(err instanceof Error ? err : new Error(String(err)));
        }
      }
    });
    socket.on("end", () => rejectOnce(new Error("browser rpc ended before response")));
    socket.on("close", (hadError) => {
      if (!settled) {
        rejectOnce(new Error(hadError ? "browser rpc socket closed after error" : "browser rpc socket closed before response"));
      }
    });
    socket.on("error", (err) => rejectOnce(err));
  });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function piToolNameFromSdk(name: string) {
  return name.startsWith(MCP_PREFIX) ? name.slice(MCP_PREFIX.length) : name;
}

export function finalizeActivePiToolCall(activeToolCall: ActiveToolCall | null, existingArguments: unknown) {
  if (!activeToolCall) return null;

  let args = isRecord(existingArguments) ? existingArguments : {};
  if (Object.keys(args).length === 0 && activeToolCall.jsonAcc.trim()) {
    try {
      const parsed = JSON.parse(activeToolCall.jsonAcc);
      if (!isRecord(parsed)) {
        throw new Error("tool_use input must be a JSON object");
      }
      args = parsed;
    } catch (err) {
      const reason = err instanceof Error ? err.message : String(err);
      throw new Error(`Failed to parse tool_use input for ${activeToolCall.name}: ${reason}; input=${activeToolCall.jsonAcc}`);
    }
  }

  return {
    type: "toolCall" as const,
    id: activeToolCall.id,
    name: activeToolCall.name,
    arguments: args,
  };
}

export function externalPiToolAcknowledgement() {
  return {
    content: [{
      type: "text" as const,
      text: "The GSD daemon accepted this tool call and will provide the actual tool result through Pi.",
    }],
    isError: false,
  };
}

/** Build a Zod shape from pi's TypeBox/JSON-Schema-ish parameters, honoring required[]. */
function piToolToSdkTool(piTool: PiTool) {
  const params = (piTool.parameters as any)?.properties ?? {};
  const required: string[] = (piTool.parameters as any)?.required ?? [];

  const shape: Record<string, z.ZodTypeAny> = {};
  for (const [key, schema] of Object.entries<any>(params)) {
    let z1: z.ZodTypeAny = schemaToZod(schema);
    if (!required.includes(key)) z1 = z1.optional();
    shape[key] = z1;
  }

  return tool(
    piTool.name,
    piTool.description || piTool.name,
    shape,
    async () => externalPiToolAcknowledgement(),
  );
}

function sessionArgFromProcess() {
  const idx = process.argv.indexOf("--session");
  if (idx >= 0 && process.argv[idx + 1]) return process.argv[idx + 1];
  return undefined;
}

function uuidFromStableKey(key: string) {
  const bytes = crypto.createHash("sha256").update(key).digest().subarray(0, 16);
  bytes[6] = (bytes[6] & 0x0f) | 0x50;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = bytes.toString("hex");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export function deriveClaudeSdkSessionId(piSessionId?: string, cwd = process.cwd()) {
  const sessionKey = piSessionId?.trim() || process.env.GSD_PI_SESSION_ID || sessionArgFromProcess() || "no-pi-session";
  const key = `${SDK_SESSION_NAMESPACE}\0${cwd}\0${sessionKey}`;
  const sessionId = uuidFromStableKey(key);
  if (!UUID_RE.test(sessionId)) {
    throw new Error("derived Claude SDK session id is not a UUID");
  }
  return sessionId;
}

function sdkContentBlocks(blocks: any[]): any[] {
  return blocks.flatMap((block): any[] => {
    if (block.type === "text") return [{ type: "text", text: block.text ?? "" }];
    if (block.type === "image") {
      return [{
        type: "image",
        source: {
          type: "base64",
          media_type: block.mimeType,
          data: block.data,
        },
      }];
    }
    return [];
  });
}

function sdkAssistantContent(blocks: any[]): any[] {
  return blocks.flatMap((block): any[] => {
    if (block.type === "text") return [{ type: "text", text: block.text ?? "" }];
    if (block.type === "thinking") {
      if (block.redacted) {
        return [{ type: "redacted_thinking", data: block.thinkingSignature ?? "" }];
      }
      if (block.thinkingSignature) {
        return [{ type: "thinking", thinking: block.thinking ?? "", signature: block.thinkingSignature }];
      }
      // SDK message input requires a signature for thinking content.
      return [{ type: "text", text: block.thinking ?? "" }];
    }
    if (block.type === "toolCall") {
      return [{
        type: "tool_use",
        id: block.id,
        name: block.name,
        input: block.arguments ?? {},
      }];
    }
    return [];
  });
}

function sdkMessageParamFromPiMessage(msg: Message) {
  if (msg.role === "assistant") {
    return {
      role: "assistant" as const,
      content: sdkAssistantContent(msg.content as any[]),
    };
  }

  if (msg.role === "toolResult") {
    return {
      role: "user" as const,
      content: [{
        type: "tool_result",
        tool_use_id: msg.toolCallId,
        content: sdkContentBlocks(msg.content as any[]),
        is_error: msg.isError,
      }],
    };
  }

  return {
    role: "user" as const,
    content: typeof msg.content === "string" ? msg.content : sdkContentBlocks(msg.content as any[]),
  };
}

function piTextContent(content: unknown) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content.flatMap((block: any) => {
    if (block?.type === "text") return [block.text ?? ""];
    if (block?.type === "image") return [`[image: ${block.mimeType ?? "unknown"}]`];
    return [];
  }).join("");
}

function renderPiHistoryForClaude(messages: Message[]) {
  const lines: string[] = [
    "Conversation history from the GSD Pi session:",
  ];

  for (const msg of messages) {
    if (msg.role === "user") {
      lines.push(`User: ${piTextContent(msg.content)}`);
    } else if (msg.role === "assistant") {
      const text = piTextContent((msg as any).content);
      if (text.trim()) lines.push(`Assistant: ${text}`);
      for (const block of (msg.content as any[]).filter((item) => item?.type === "toolCall")) {
        lines.push(`Assistant tool call: ${block.name} ${JSON.stringify(block.arguments ?? {})}`);
      }
    } else if (msg.role === "toolResult") {
      const status = msg.isError ? "error" : "success";
      lines.push(`Tool result (${msg.toolName} ${status}):`);
      lines.push(piTextContent(msg.content));
    }
  }

  lines.push("Continue from the latest message. Use tool results as already completed work.");
  return lines.join("\n");
}

export function buildClaudePromptMessages(
  messages: Message[],
  sdkSessionId: string,
  replayHistory = false,
): SdkPromptInputMessage[] {
  const lastIndex = messages.length - 1;
  if (lastIndex < 0) throw new Error("Claude SDK prompt requires at least one message");

  if (replayHistory) {
    return [{
      type: "user",
      message: {
        role: "user" as const,
        content: renderPiHistoryForClaude(messages),
      },
      parent_tool_use_id: null,
      session_id: sdkSessionId,
    }];
  }

  const latestMessage = messages[lastIndex];
  if (latestMessage.role === "toolResult") {
    return [{
      type: "user",
      message: {
        role: "user" as const,
        content: renderPiHistoryForClaude(messages),
      },
      parent_tool_use_id: null,
      session_id: sdkSessionId,
    }];
  }

  return messages.slice(lastIndex).map((msg) => {
    const prompt: SDKUserMessage = {
      type: "user",
      message: sdkMessageParamFromPiMessage(msg) as any,
      parent_tool_use_id: null,
      session_id: sdkSessionId,
    };

    return prompt;
  });
}

function buildClaudePromptIterable(
  messages: Message[],
  sdkSessionId: string,
  replayHistory = false,
): AsyncIterable<SdkPromptInputMessage> {
  const promptMessages = buildClaudePromptMessages(messages, sdkSessionId, replayHistory);
  return (async function* () {
    for (const message of promptMessages) {
      yield message;
    }
  })();
}

function streamClaudeSdk(
  model: Model<any>,
  context: Context,
  options?: SimpleStreamOptions,
): AssistantMessageEventStream {
  if (process.env.GSD_WARM_CLAUDE_SDK === "1") {
    return streamWarmClaudeSdk(model, context, options);
  }

  const stream = createAssistantMessageEventStream();

  (async () => {
    const output: AssistantMessage = {
      role: "assistant",
      content: [],
      api: model.api,
      provider: model.provider,
      model: model.id,
      usage: {
        input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0,
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
      },
      stopReason: "stop",
      timestamp: Date.now(),
    };

    const sdkAbort = new AbortController();
    options?.signal?.addEventListener("abort", () => sdkAbort.abort(), { once: true });
    let sdkStderrTail = "";
    const rememberSdkStderr = (chunk: unknown) => {
      sdkStderrTail = `${sdkStderrTail}${String(chunk)}`.slice(-4_000);
    };

    const browserGrant = browserGrantFromEnv();
    const piTools = mergeClaudeCliTools(context.tools as PiTool[] | undefined, browserGrant);
    const sdkTools = piTools.map((t) => piToolToSdkTool(t));
    emitProviderContextDiagnostics("claude-cli", model, context, piTools as any[]);

    const piMcp = createSdkMcpServer({
      name: "pi-tools",
      version: "0.0.1",
      tools: sdkTools,
    });

    const allowedTools = sdkTools.map((t: any) => `${MCP_PREFIX}${(t as any).name}`);

    stream.push({ type: "start", partial: output });
    const handlers = createClaudeSdkRunHandlers(stream, output, model, options, () => sdkStderrTail);

    try {
      const sdkOptions: SdkOptions = {
        includePartialMessages: true,
        persistSession: false,
        settingSources: [],
        allowedTools,
        disallowedTools: CLAUDE_BUILTINS,
        mcpServers: { "pi-tools": piMcp },
        abortController: sdkAbort,
        permissionMode: "bypassPermissions",
        stderr: rememberSdkStderr,
      };
      if (context.systemPrompt && !isMinimalToolProfile()) {
        sdkOptions.systemPrompt = context.systemPrompt;
      }

      const sdkSessionId = crypto.randomUUID();
      const replayHistory = context.messages.length > 1;
      sdkOptions.sessionId = sdkSessionId;

      const promptIter = buildClaudePromptIterable(context.messages, sdkSessionId, replayHistory);
      const q = query({ prompt: promptIter, options: sdkOptions });

      for await (const msg of q as AsyncIterable<SDKMessage>) {
        handlers.handleMessage(msg);
      }

      handlers.finish();
    } catch (err: any) {
      handlers.fail(err);
    }
  })();

  return stream;
}

function assistantMessageHasPayload(message: AssistantMessage) {
  return message.content.some((block: any) => {
    if (block?.type === "text") return typeof block.text === "string" && block.text.trim().length > 0;
    if (block?.type === "toolCall") return typeof block.name === "string" && block.name.length > 0;
    return Boolean(block);
  });
}

export function createClaudeSdkRunHandlers(
  stream: AssistantMessageEventStream,
  output: AssistantMessage,
  model: Model<any>,
  options?: SimpleStreamOptions,
  diagnostics?: () => string,
) {
  let activeTextIndex: number | null = null;
  let activeToolCall: ActiveToolCall | null = null;
  let streamEndedForToolUse = false;
  let streamClosed = false;
  const sdkMessageTypes = new Map<string, number>();
  let sdkResultSummary = "";

  const diagnosticDetails = () => {
    const lines: string[] = [];
    const messageTypes = Array.from(sdkMessageTypes.entries())
      .map(([type, count]) => `${type}:${count}`)
      .join(", ");
    if (messageTypes) lines.push(`Claude SDK messages: ${messageTypes}`);
    if (sdkResultSummary) lines.push(`Claude SDK result: ${sdkResultSummary}`);
    const extra = diagnostics?.().trim();
    if (extra) lines.push(`Claude SDK stderr:\n${extra}`);
    return lines.join("\n\n");
  };

  const rememberSdkMessage = (msg: SDKMessage) => {
    const type = (msg as any)?.type ?? "unknown";
    sdkMessageTypes.set(type, (sdkMessageTypes.get(type) ?? 0) + 1);
    if (type !== "result") return;

    const result = msg as any;
    const parts = [
      `subtype=${String(result.subtype ?? "unknown")}`,
      `is_error=${String(Boolean(result.is_error))}`,
      `stop_reason=${String(result.stop_reason ?? "unknown")}`,
      `num_turns=${String(result.num_turns ?? "unknown")}`,
    ];
    if (typeof result.result === "string" && result.result.trim().length > 0) {
      parts.push(`result=${result.result.trim().slice(0, 1_000)}`);
    }
    if (Array.isArray(result.errors) && result.errors.length > 0) {
      parts.push(`errors=${result.errors.map((err: unknown) => String(err)).join(" | ").slice(0, 1_000)}`);
    }
    sdkResultSummary = parts.join(", ");
  };

  const appendFinalResultText = (text: string) => {
    if (assistantMessageHasPayload(output)) return;
    const contentIndex = output.content.length;
    output.content.push({ type: "text", text });
    stream.push({ type: "text_start", contentIndex, partial: output });
    stream.push({ type: "text_delta", contentIndex, delta: text, partial: output });
    stream.push({ type: "text_end", contentIndex, content: text, partial: output });
  };

  const closeStreamForToolUse = () => {
    if (streamClosed) return;
    stream.push({ type: "done", reason: "toolUse", message: output });
    stream.end();
    streamClosed = true;
  };

  const handleMessage = (msg: SDKMessage) => {
    rememberSdkMessage(msg);
    applyUsageFromSdkMessage(output.usage, msg, (model as any).cost);
    if (streamEndedForToolUse) return;
    if ((msg as any)?.type === "result") {
      const resultText = (msg as any)?.result;
      if (typeof resultText === "string" && resultText.trim().length > 0) {
        appendFinalResultText(resultText);
      }
      return;
    }
    if (msg.type !== "stream_event") return;
    const ev = (msg as any).event;
    if (ev?.type === "content_block_start") {
      const block = ev.content_block;
      if (block?.type === "text") {
        output.content.push({ type: "text", text: "" });
        activeTextIndex = output.content.length - 1;
        stream.push({ type: "text_start", contentIndex: activeTextIndex, partial: output });
      } else if (block?.type === "tool_use") {
        const fullName = block.name as string;
        const piName = fullName.startsWith(MCP_PREFIX) ? fullName.slice(MCP_PREFIX.length) : fullName;
        const id = block.id as string;
        output.content.push({ type: "toolCall", id, name: piName, arguments: {} });
        activeToolCall = { idx: output.content.length - 1, id, name: piName, jsonAcc: "" };
        stream.push({ type: "toolcall_start", contentIndex: activeToolCall.idx, partial: output });
      }
    } else if (ev?.type === "content_block_delta") {
      const delta = ev.delta;
      if (delta?.type === "text_delta" && activeTextIndex !== null) {
        const text = delta.text as string;
        const blk = output.content[activeTextIndex] as any;
        blk.text += text;
        stream.push({ type: "text_delta", contentIndex: activeTextIndex, delta: text, partial: output });
      } else if (delta?.type === "input_json_delta" && activeToolCall) {
        const chunk = delta.partial_json as string;
        activeToolCall.jsonAcc += chunk;
        try {
          const parsed = JSON.parse(activeToolCall.jsonAcc);
          (output.content[activeToolCall.idx] as any).arguments = parsed;
        } catch {}
        stream.push({ type: "toolcall_delta", contentIndex: activeToolCall.idx, delta: chunk, partial: output });
      }
    } else if (ev?.type === "content_block_stop") {
      if (activeTextIndex !== null) {
        const finalText = (output.content[activeTextIndex] as any).text;
        stream.push({ type: "text_end", contentIndex: activeTextIndex, content: finalText, partial: output });
        activeTextIndex = null;
      }
      if (activeToolCall) {
        const blk = output.content[activeToolCall.idx] as any;
        const toolCall = finalizeActivePiToolCall(activeToolCall, blk.arguments);
        if (toolCall) {
          blk.arguments = toolCall.arguments;
          stream.push({ type: "toolcall_end", contentIndex: activeToolCall.idx, toolCall, partial: output });
          activeToolCall = null;
          output.stopReason = "toolUse";
          ensureNonZeroUsageForAbortedToolTurn(output.usage, output.content, (model as any).cost);
          streamEndedForToolUse = true;
        }
      }
    }
  };

  const finish = () => {
    if (streamEndedForToolUse) {
      closeStreamForToolUse();
      return;
    }
    if (!assistantMessageHasPayload(output)) {
      output.stopReason = "error";
      const details = diagnosticDetails();
      output.errorMessage = details
        ? `Claude SDK completed without assistant content.\n\n${details}`
        : "Claude SDK completed without assistant content.";
      stream.push({ type: "error", reason: "error", error: output });
      stream.end();
      return;
    }
    output.stopReason = "stop";
    stream.push({ type: "done", reason: "stop", message: output });
    stream.end();
  };

  const fail = (err: unknown) => {
    if (streamEndedForToolUse) {
      closeStreamForToolUse();
      return;
    }
    output.stopReason = options?.signal?.aborted ? "aborted" : "error";
    output.errorMessage = err instanceof Error ? err.message : String(err);
    stream.push({ type: "error", reason: output.stopReason as any, error: output });
    stream.end();
  };

  return { handleMessage, finish, fail };
}

function streamWarmClaudeSdk(
  model: Model<any>,
  context: Context,
  options?: SimpleStreamOptions,
): AssistantMessageEventStream {
  const stream = createAssistantMessageEventStream();
  const output: AssistantMessage = {
    role: "assistant",
    content: [],
    api: model.api,
    provider: model.provider,
    model: model.id,
    usage: {
      input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
    },
    stopReason: "stop",
    timestamp: Date.now(),
  };
  stream.push({ type: "start", partial: output });

  const browserGrant = browserGrantFromEnv();
  const piTools = mergeClaudeCliTools(context.tools as PiTool[] | undefined, browserGrant);
  const sdkTools = piTools.map((t) => piToolToSdkTool(t));
  emitProviderContextDiagnostics("claude-cli", model, context, piTools as any[]);
  const allowedTools = sdkTools.map((t: any) => `${MCP_PREFIX}${(t as any).name}`);
  const piMcp = createSdkMcpServer({ name: "pi-tools", version: "0.0.1", tools: sdkTools });
  const sdkAbort = new AbortController();
  options?.signal?.addEventListener("abort", () => sdkAbort.abort(), { once: true });
  let sdkStderrTail = "";
  const rememberSdkStderr = (chunk: unknown) => {
    sdkStderrTail = `${sdkStderrTail}${String(chunk)}`.slice(-4_000);
  };

  const sdkOptions: SdkOptions = {
    includePartialMessages: true,
    persistSession: false,
    settingSources: [],
    allowedTools,
    disallowedTools: CLAUDE_BUILTINS,
    mcpServers: { "pi-tools": piMcp },
    abortController: sdkAbort,
    permissionMode: "bypassPermissions",
    stderr: rememberSdkStderr,
  };
  if (context.systemPrompt && !isMinimalToolProfile()) sdkOptions.systemPrompt = context.systemPrompt;

  const signature = warmClaudeOptionsKey(model, context);
  if (warmClaudeOptionsSignature && warmClaudeOptionsSignature !== signature) {
    resetWarmClaudeWorker();
  }
  if (!warmClaudeWorker) {
    warmClaudeOptionsSignature = signature;
    warmClaudeWorker = new WarmClaudeSdkWorker(query, () => sdkOptions);
  }

  const handlers = createClaudeSdkRunHandlers(stream, output, model, options, () => sdkStderrTail);
  const sdkSessionId = deriveClaudeSdkSessionId(undefined, process.cwd());
  const replayHistory = context.messages.length > 1 && !warmClaudeWorker.hasStarted();
  const messages = buildClaudePromptMessages(context.messages, sdkSessionId, replayHistory);

  warmClaudeWorker.turn({ messages, onMessage: handlers.handleMessage })
    .then(() => handlers.finish())
    .catch((err) => {
      resetWarmClaudeWorker();
      handlers.fail(err);
    });

  return stream;
}

// -----------------------------------------------------------------
// ask_human tool
//
// Pauses the agent and routes the question to whoever is driving pi.
// In the GSD daemon path, this surfaces as an extension_ui_request[method=input]
// that the daemon's pi.Executor intercepts and translates into protocol.Question.
// -----------------------------------------------------------------

const AskHumanParams = Type.Object({
  question: Type.String({ description: "The question to ask the human." }),
  context: Type.Optional(Type.String({ description: "Optional background context." })),
});

function registerAskHumanTool(pi: ExtensionAPI, registeredTools?: ToolRegistrationDiagnostic[]) {
  const definition = {
    name: "ask_human",
    label: "Ask the human",
    description:
      "Pause and ask the human a question. Use when ambiguity blocks progress, when two valid choices need a decision, or when a destructive operation needs confirmation. The human's answer is returned as the tool result.",
    parameters: AskHumanParams,
    async execute(_toolCallId, params, signal, _onUpdate, ctx: any) {
      const title = params.context ? `${params.context}\n\n${params.question}` : params.question;
      const answer = await ctx.ui.input(title, "Type your answer...", { signal });
      if (answer === undefined) {
        return {
          content: [{ type: "text", text: "(human cancelled or did not answer)" }],
          isError: true,
          details: {},
        };
      }
      return {
        content: [{ type: "text", text: answer }],
        isError: false,
        details: {},
      };
    },
  };
  pi.registerTool(definition);
  registeredTools?.push(describeRegisteredTool("human", definition));
}

function registerBrowserTool(pi: ExtensionAPI, registeredTools?: ToolRegistrationDiagnostic[]) {
  const definition = browserToolDefinition();
  const registeredDefinition = {
    name: definition.name,
    label: definition.label,
    description: definition.description,
    parameters: definition.parameters,
    async execute(_toolCallId: string, params: any, signal?: AbortSignal) {
      const browserGrant = browserGrantFromEnv();
      if (!browserGrant) {
        return {
          content: [{ type: "text", text: "No task-scoped browser grant is active." }],
          isError: true,
          details: {},
        };
      }
      try {
        const result = await browserRpc(browserGrant.browserId, params.method, params.params ?? {}, signal);
        return {
          content: [{ type: "text", text: JSON.stringify(result) }],
          isError: false,
          details: { browserId: browserGrant.browserId, grantId: browserGrant.grantId },
        };
      } catch (err) {
        return {
          content: [{ type: "text", text: err instanceof Error ? err.message : String(err) }],
          isError: true,
          details: { browserId: browserGrant.browserId, grantId: browserGrant.grantId },
        };
      }
    },
  };
  pi.registerTool(registeredDefinition as any);
  registeredTools?.push(describeRegisteredTool("browser", registeredDefinition));
}

export default function (pi: ExtensionAPI) {
  const subagentAllowedTools = parseAllowedTools(process.env.GSD_SUBAGENT_ALLOWED_TOOLS);
  const browserGrant = browserGrantFromEnv();
  const profile = toolProfile();
  const registeredTools: ToolRegistrationDiagnostic[] = [];
  if (!isMinimalToolProfile()) {
    registerAskHumanTool(pi, registeredTools);
    if (browserGrant && (isToolAllowed("browser", subagentAllowedTools) || !hasSubagentToolPolicy())) {
      registerBrowserTool(pi, registeredTools);
    }
    registerTrackedTool(pi, registeredTools, "human", askUserQuestionsTool as any);
    for (const backgroundTool of backgroundTools) {
      registerVisibleTool(pi, subagentAllowedTools, "shell", backgroundTool as any, registeredTools);
    }
    if (isToolAllowed("plan", subagentAllowedTools) || !hasSubagentToolPolicy()) {
      registerPlanTools(pi as any, process.env, (category: string, definition: any) => {
        registeredTools.push(describeRegisteredTool(category, definition));
      });
    }
    if (!hasSubagentToolPolicy()) {
      registerSubagentTool(pi as any, process.env, {
        onRegister: (category: string, definition: any) => {
          registeredTools.push(describeRegisteredTool(category, definition));
        },
      });
    }
  }
  emitScaffoldDiagnostics(profile, registeredTools);
  pi.registerProvider("claude-cli", {
    baseUrl: "http://localhost/unused",
    apiKey: "CLAUDE_CLI_KEY",
    api: "anthropic-messages" as any,
    models: [
      {
        id: "claude-opus-4-7",
        name: "Claude Opus 4.7 (via SDK)",
        reasoning: false,
        input: ["text", "image"],
        cost: { input: 5.0, output: 25.0, cacheRead: 0.50, cacheWrite: 6.25 },
        contextWindow: 1_000_000,
        maxTokens: 128_000,
      },
      {
        id: "claude-opus-4-6",
        name: "Claude Opus 4.6 (via SDK)",
        reasoning: false,
        input: ["text", "image"],
        cost: { input: 5.0, output: 25.0, cacheRead: 0.50, cacheWrite: 6.25 },
        contextWindow: 1_000_000,
        maxTokens: 128_000,
      },
      {
        id: "claude-sonnet-4-6",
        name: "Claude Sonnet 4.6 (via SDK)",
        reasoning: false,
        input: ["text", "image"],
        // Anthropic Sonnet 4.x list pricing per 1M tokens (as of 2025-08, public docs).
        cost: { input: 3.0, output: 15.0, cacheRead: 0.30, cacheWrite: 3.75 },
        contextWindow: 1_000_000,
        maxTokens: 8_192,
      },
      {
        id: "claude-haiku-4-5-20251001",
        name: "Claude Haiku 4.5 (via SDK)",
        reasoning: false,
        input: ["text", "image"],
        cost: { input: 1.0, output: 5.0, cacheRead: 0.10, cacheWrite: 1.25 },
        contextWindow: 200_000,
        maxTokens: 64_000,
      },
    ],
    streamSimple: streamClaudeSdk,
  });
  registerCodexAppServerProvider(pi);
  registerOpenRouterProvider(pi);
}
