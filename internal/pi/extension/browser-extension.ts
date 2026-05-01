import net from "node:net";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { Type } from "@sinclair/typebox";
import {
  BROWSER_METHOD_CATEGORY,
  BROWSER_TOOL_CATEGORIES,
  BROWSER_TOOL_METHODS,
  BrowserToolCategorySchema,
  BrowserToolMethodSchema,
  type BrowserToolMethod,
} from "./browser-methods.generated.js";

const extensionDir = dirname(fileURLToPath(import.meta.url));

export type BrowserGrant = {
  grantId: string;
  sessionId: string;
  taskId: string;
  channelId: string;
  projectId: string;
  machineId: string;
  expiresAt: string;
};

const BrowserToolParams = Type.Object({
  method: BrowserToolMethodSchema,
  category: Type.Optional(BrowserToolCategorySchema),
  intent: Type.Optional(Type.String()),
  params: Type.Optional(Type.Record(Type.String(), Type.Any())),
});

export function browserGrantFromEnv(): BrowserGrant | undefined {
  const grantId = process.env.GSD_BROWSER_GRANT_ID;
  const sessionId = process.env.GSD_BROWSER_SESSION_ID;
  const taskId = process.env.GSD_TASK_ID;
  const channelId = process.env.GSD_CHANNEL_ID;
  const projectId = process.env.GSD_PROJECT_ID;
  const machineId = process.env.GSD_MACHINE_ID;
  const expiresAt = process.env.GSD_BROWSER_GRANT_EXPIRES_AT;
  if (!grantId || !sessionId || !taskId || !channelId || !projectId || !machineId || !expiresAt) {
    return undefined;
  }
  return { grantId, sessionId, taskId, channelId, projectId, machineId, expiresAt };
}

export function browserToolDefinition() {
  return {
    name: "gsd_browser",
    label: "GSD Browser",
    description:
      "Use the active task-scoped GSD shared browser for browser automation, rendered UI verification, navigation, snapshots, ref-based interaction, screenshots, console/network inspection, visual diffs, traces, and artifacts. Use snapshot refs before clicking/filling. Use bare method names such as navigate, snapshot, click_ref, console, and visual_diff.",
    promptSnippet:
      "GSD Browser is available for website interaction, rendered UI evidence, screenshots, console/network checks, auth flows, and responsive testing. Load the gsd-browser skill when browser behavior matters.",
    promptGuidelines: [
      "Use gsd_browser proactively when rendered browser behavior is evidence.",
      "Run snapshot before ref-based interaction and re-snapshot after page changes.",
      "State intent before multi-step browser work.",
      "Request approval for credential, payment, destructive, external-effect, and network-mutation actions.",
    ],
    parameters: BrowserToolParams,
    input_schema: {
      type: "object",
      additionalProperties: false,
      properties: {
        method: { type: "string", enum: BROWSER_TOOL_METHODS },
        category: { type: "string", enum: BROWSER_TOOL_CATEGORIES },
        intent: { type: "string" },
        params: { type: "object", additionalProperties: true },
      },
      required: ["method"],
    },
  };
}

export function browserMethodCategory(method: string) {
  return BROWSER_METHOD_CATEGORY[method as BrowserToolMethod] ?? "inspection";
}

export function browserActionSummary(method: string, params: Record<string, unknown> = {}) {
  if (method === "navigate" && typeof params.url === "string") return `Navigate to ${params.url}`;
  if (method === "snapshot") return "Snapshot page";
  if (method === "click_ref" && typeof params.ref === "string") return `Click ${params.ref}`;
  if (method === "fill_ref" && typeof params.ref === "string") return `Fill ${params.ref}`;
  if (method === "console") return "Check console";
  if (method === "network") return "Check network";
  return method.replaceAll("_", " ");
}

async function browserRpc(grant: BrowserGrant, toolCallId: string, method: string, params: unknown, signal?: AbortSignal) {
  const socketPath = process.env.GSD_DAEMON_BROWSER_RPC_SOCKET;
  if (!socketPath) throw new Error("daemon browser RPC socket is unavailable");
  const payload = Buffer.from(JSON.stringify({
    jsonrpc: "2.0",
    id: Date.now(),
    method: "browser_tool",
    params: { ...grant, toolUseId: toolCallId, method, params: params ?? {} },
  }));
  const header = Buffer.alloc(4);
  header.writeUInt32BE(payload.length, 0);

  return await new Promise<unknown>((resolve, reject) => {
    const socket = net.createConnection(socketPath);
    const chunks: Buffer[] = [];
    let expected = 0;
    let settled = false;
    const rejectOnce = (err: Error) => {
      if (settled) return;
      settled = true;
      socket.destroy();
      reject(err);
    };
    const resolveOnce = (value: unknown) => {
      if (settled) return;
      settled = true;
      socket.end();
      resolve(value);
    };
    if (signal?.aborted) return rejectOnce(new Error("browser tool aborted"));
    signal?.addEventListener("abort", () => rejectOnce(new Error("browser tool aborted")), { once: true });
    socket.setTimeout(30_000, () => rejectOnce(new Error("browser tool timed out")));
    socket.on("connect", () => socket.write(Buffer.concat([header, payload])));
    socket.on("data", (chunk) => {
      chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
      const all = Buffer.concat(chunks);
      if (expected === 0 && all.length >= 4) expected = all.readUInt32BE(0);
      if (expected > 16 * 1024 * 1024) return rejectOnce(new Error(`browser rpc frame too large: ${expected}`));
      if (expected > 0 && all.length >= expected + 4) {
        const response = JSON.parse(all.subarray(4, expected + 4).toString("utf8"));
        if (response.error) rejectOnce(new Error(response.error.message ?? "browser tool failed"));
        else resolveOnce(response.result ?? {});
      }
    });
    socket.on("error", rejectOnce);
    socket.on("close", () => {
      if (!settled) rejectOnce(new Error("browser rpc socket closed before response"));
    });
  });
}

export function registerBrowserExtension(pi: ExtensionAPI) {
  if (typeof (pi as any).on === "function") {
    (pi as any).on("resources_discover", () => ({
      skillPaths: [join(extensionDir, "gsd-browser-skill", "SKILL.md")],
    }));
  }

  pi.registerTool({
    ...browserToolDefinition(),
    async execute(toolCallId: string, params: any, signal?: AbortSignal) {
      const grant = browserGrantFromEnv();
      const method = params.method as string;
      const category = params.category ?? browserMethodCategory(method);
      const summary = browserActionSummary(method, params.params ?? {});
      if (!grant) {
        const code = process.env.GSD_BROWSER_RUNTIME_ERROR_CODE || "browser_context_unavailable";
        const message = process.env.GSD_BROWSER_RUNTIME_ERROR_MESSAGE || "GSD Browser runtime is unavailable for this task.";
        return {
          content: [{ type: "text", text: message }],
          isError: true,
          details: { toolCallId, method, category, summary, code },
        };
      }
      try {
        const result = await browserRpc(grant, toolCallId, method, params.params ?? {}, signal);
        return {
          content: [{ type: "text", text: JSON.stringify(result) }],
          isError: false,
          details: { ...grant, toolCallId, method, category, summary, safeResult: result },
        };
      } catch (err) {
        return {
          content: [{ type: "text", text: err instanceof Error ? err.message : String(err) }],
          isError: true,
          details: { ...grant, toolCallId, method, category, summary },
        };
      }
    },
  } as any);
}
