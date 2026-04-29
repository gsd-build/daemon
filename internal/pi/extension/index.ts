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
  getSessionMessages,
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
import { registerPlanTools } from "./plan-tools.js";
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

type SdkPromptInputMessage = SDKUserMessage | SDKUserMessageReplay;

type BrowserGrant = {
  grantId: string;
  browserId: string;
  sessionId: string;
};

const BrowserToolParams = Type.Object({
  method: Type.String({ description: "Browser operation to execute." }),
  params: Type.Optional(Type.Record(Type.String(), Type.Any())),
});

function browserToolDefinition() {
  return {
    name: "gsd_browser",
    label: "GSD Browser",
    description: "Use the task-scoped GSD shared browser session.",
    parameters: BrowserToolParams,
    input_schema: {
      type: "object",
      additionalProperties: false,
      properties: {
        method: { type: "string" },
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
  const merged: PiTool[] = [];
  const seenNames = new Set<string>();
  const browserTools = buildClaudeCliBrowserTools({ browserGrant }) as unknown as PiTool[];

  for (const toolDef of [...(contextTools ?? []), ...browserTools]) {
    const name = piToolName(toolDef);
    if (name) {
      if (seenNames.has(name)) continue;
      seenNames.add(name);
    }
    merged.push(toolDef);
  }

  return merged;
}

function browserGrantFromEnv() {
  const grantId = process.env.GSD_BROWSER_GRANT_ID;
  const browserId = process.env.GSD_BROWSER_ID;
  const sessionId = process.env.GSD_BROWSER_SESSION_ID;
  if (!grantId || !browserId || !sessionId) return undefined;
  return { grantId, browserId, sessionId };
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

class PiToolCallSurfacing extends Error {
  toolName: string;
  args: unknown;

  constructor(toolName: string, args: unknown) {
    super(`pi-tool-call: ${toolName}`);
    this.toolName = toolName;
    this.args = args;
  }
}

/** Build a Zod shape from pi's TypeBox/JSON-Schema-ish parameters, honoring required[]. */
function piToolToSdkTool(piTool: PiTool, surface: (name: string, args: unknown) => never) {
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
    async (args: any) => {
      surface(piTool.name, args);
      return { content: [{ type: "text" as const, text: "unreachable" }] };
    },
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

function replayUuid(sdkSessionId: string, msg: Message, index: number): SDKUserMessageReplay["uuid"] {
  return uuidFromStableKey(`${SDK_SESSION_NAMESPACE}:replay\0${sdkSessionId}\0${index}\0${msg.role}\0${msg.timestamp}`) as SDKUserMessageReplay["uuid"];
}

export function buildClaudePromptMessages(
  messages: Message[],
  sdkSessionId: string,
  replayHistory = false,
): SdkPromptInputMessage[] {
  const lastIndex = messages.length - 1;
  if (lastIndex < 0) throw new Error("Claude SDK prompt requires at least one message");

  const firstIndex = replayHistory ? 0 : lastIndex;
  return messages.slice(firstIndex).map((msg, offset) => {
    const index = firstIndex + offset;
    const replay = replayHistory && index < lastIndex;
    const prompt: SDKUserMessage = {
      type: "user",
      message: sdkMessageParamFromPiMessage(msg) as any,
      parent_tool_use_id: msg.role === "toolResult" ? msg.toolCallId : null,
      session_id: sdkSessionId,
    };

    if (!replay) return prompt;

    return {
      ...prompt,
      uuid: replayUuid(sdkSessionId, msg, index),
      session_id: sdkSessionId,
      isReplay: true,
      shouldQuery: false,
    } as SDKUserMessageReplay;
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

async function claudeSdkSessionExists(sdkSessionId: string) {
  try {
    const messages = await getSessionMessages(sdkSessionId, {
      dir: process.cwd(),
      limit: 1,
      includeSystemMessages: true,
    });
    return messages.length > 0;
  } catch {
    return false;
  }
}

function streamClaudeSdk(
  model: Model<any>,
  context: Context,
  options?: SimpleStreamOptions,
): AssistantMessageEventStream {
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

    let surfaced: PiToolCallSurfacing | null = null;
    const sdkAbort = new AbortController();
    options?.signal?.addEventListener("abort", () => sdkAbort.abort(), { once: true });

    let activeTextIndex: number | null = null;
    let activeToolCall: { idx: number; id: string; name: string; jsonAcc: string } | null = null;

    const browserGrant = browserGrantFromEnv();
    const piTools = mergeClaudeCliTools(context.tools as PiTool[] | undefined, browserGrant);
    const sdkTools = piTools.map((t) =>
      piToolToSdkTool(t, (name, args) => {
        surfaced = new PiToolCallSurfacing(name, args);
        sdkAbort.abort();
        throw surfaced;
      }),
    );

    const piMcp = createSdkMcpServer({
      name: "pi-tools",
      version: "0.0.1",
      tools: sdkTools,
    });

    const allowedTools = sdkTools.map((t: any) => `${MCP_PREFIX}${(t as any).name}`);

    stream.push({ type: "start", partial: output });

    try {
      const sdkOptions: SdkOptions = {
        includePartialMessages: true,
        persistSession: true,
        settingSources: [],
        allowedTools,
        disallowedTools: CLAUDE_BUILTINS,
        mcpServers: { "pi-tools": piMcp },
        abortController: sdkAbort,
        permissionMode: "bypassPermissions",
        stderr: () => {},
      };
      if (context.systemPrompt) {
        sdkOptions.systemPrompt = context.systemPrompt;
      }

      const sdkSessionId = deriveClaudeSdkSessionId(options?.sessionId);
      const resumeClaudeSession = await claudeSdkSessionExists(sdkSessionId);
      const replayHistory = !resumeClaudeSession && context.messages.length > 1;
      if (resumeClaudeSession) {
        sdkOptions.resume = sdkSessionId;
      } else {
        sdkOptions.sessionId = sdkSessionId;
      }

      const promptIter = buildClaudePromptIterable(context.messages, sdkSessionId, replayHistory);
      const q = query({ prompt: promptIter, options: sdkOptions });

      for await (const msg of q as AsyncIterable<SDKMessage>) {
        applyUsageFromSdkMessage(output.usage, msg, (model as any).cost);
        if (msg.type === "stream_event") {
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
            // tool_use content_block_stop: handled when sentinel fires inside the MCP handler.
          }
        }
      }

      // SDK iterator drained without sentinel; Claude finished a pure-text turn.
      output.stopReason = "stop";
      stream.push({ type: "done", reason: "stop", message: output });
      stream.end();
    } catch (err: any) {
      // Sentinel: gracefully end with toolUse so pi runs the tool.
      if (surfaced) {
        if (activeToolCall) {
          const blk = output.content[activeToolCall.idx] as any;
          // Use the args we captured from the input_json deltas if available;
          // otherwise fall back to the args the MCP handler received.
          if (Object.keys(blk.arguments || {}).length === 0) {
            blk.arguments = (surfaced as PiToolCallSurfacing).args;
          }
          stream.push({
            type: "toolcall_end",
            contentIndex: activeToolCall.idx,
            toolCall: { type: "toolCall", id: activeToolCall.id, name: activeToolCall.name, arguments: blk.arguments },
            partial: output,
          });
          activeToolCall = null;
        }
        output.stopReason = "toolUse";
        ensureNonZeroUsageForAbortedToolTurn(output.usage, output.content, (model as any).cost);
        stream.push({ type: "done", reason: "toolUse", message: output });
        stream.end();
        return;
      }
      output.stopReason = options?.signal?.aborted ? "aborted" : "error";
      output.errorMessage = err instanceof Error ? err.message : String(err);
      stream.push({ type: "error", reason: output.stopReason as any, error: output });
      stream.end();
    }
  })();

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

function registerAskHumanTool(pi: ExtensionAPI) {
  pi.registerTool({
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
  });
}

function registerBrowserTool(pi: ExtensionAPI) {
  const definition = browserToolDefinition();
  pi.registerTool({
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
  } as any);
}

export default function (pi: ExtensionAPI) {
  registerAskHumanTool(pi);
  registerBrowserTool(pi);
  pi.registerTool(askUserQuestionsTool as any);
  registerPlanTools(pi as any);
  pi.registerProvider("claude-cli", {
    baseUrl: "http://localhost/unused",
    apiKey: "CLAUDE_CLI_KEY",
    api: "anthropic-messages" as any,
    models: [
      {
        id: "claude-opus-4-6",
        name: "Claude Opus 4.6 (via SDK)",
        reasoning: false,
        input: ["text", "image"],
        cost: { input: 15.0, output: 75.0, cacheRead: 1.50, cacheWrite: 18.75 },
        contextWindow: 1_000_000,
        maxTokens: 8_192,
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
    ],
    streamSimple: streamClaudeSdk,
  });
}
