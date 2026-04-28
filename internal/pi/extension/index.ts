/**
 * Pi provider extension for the GSD daemon.
 *
 * The extension registers a Claude Agent SDK backed provider and an
 * ask_human tool. Pi owns tool execution and UI routing; the provider streams
 * Claude SDK output into pi's assistant message event stream and returns
 * tool calls to pi for execution.
 */

import {
  createSdkMcpServer,
  query,
  tool,
  type Options as SdkOptions,
  type SDKMessage,
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

class PiToolCallSurfacing extends Error {
  constructor(public toolName: string, public args: unknown) {
    super(`pi-tool-call: ${toolName}`);
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

/**
 * Convert pi's Message[] history into a stream of SDKUserMessage prompts.
 */
function buildClaudePromptIterable(messages: Message[], systemPrompt?: string): AsyncIterable<any> {
  const transcript: string[] = [];
  if (systemPrompt) transcript.push(`(system: ${systemPrompt})\n`);

  for (const msg of messages) {
    if (msg.role === "user") {
      const text =
        typeof msg.content === "string"
          ? msg.content
          : (msg.content as any[]).filter((b) => b.type === "text").map((b) => b.text).join("\n");
      if (text.trim()) transcript.push(`User: ${text}`);
    } else if (msg.role === "assistant") {
      for (const block of msg.content as any[]) {
        if (block.type === "text" && block.text.trim()) {
          transcript.push(`Assistant: ${block.text}`);
        } else if (block.type === "toolCall") {
          transcript.push(
            `[Tool call by you]\nname: ${block.name}\nid: ${block.id}\narguments: ${JSON.stringify(block.arguments)}`,
          );
        }
      }
    } else if (msg.role === "toolResult") {
      const tr: any = msg;
      const text = (tr.content as any[]).filter((b) => b.type === "text").map((b) => b.text).join("\n");
      transcript.push(
        `[Result from your tool call]\nname: ${tr.toolName}\nid: ${tr.toolCallId}\nresult:\n${text}`,
      );
    }
  }

  // Determine the latest user request. If the last actionable message is a
  // toolResult, instruct Claude to continue. Otherwise the last user message
  // is the request.
  const lastMsg = messages[messages.length - 1];
  let instruction: string;
  if (lastMsg?.role === "toolResult") {
    instruction = "(continue based on the tool result above; do not repeat the tool call)";
  } else {
    instruction = "(respond to the latest user message; you may call tools as needed)";
  }

  const finalPrompt = transcript.join("\n\n") + "\n\n" + instruction;

  return (async function* () {
    yield {
      type: "user" as const,
      message: { role: "user" as const, content: finalPrompt },
      parent_tool_use_id: null,
      session_id: "pi-rpc-extension",
    };
  })();
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

    const piTools = (context.tools as PiTool[] | undefined) ?? [];
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
        persistSession: false,
        settingSources: [],
        allowedTools,
        disallowedTools: CLAUDE_BUILTINS,
        mcpServers: { "pi-tools": piMcp },
        abortController: sdkAbort,
        permissionMode: "bypassPermissions",
        stderr: () => {},
      };

      const promptIter = buildClaudePromptIterable(context.messages, context.systemPrompt);
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
import { Type } from "@sinclair/typebox";

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

export default function (pi: ExtensionAPI) {
  registerAskHumanTool(pi);
  pi.registerTool(askUserQuestionsTool as any);
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
