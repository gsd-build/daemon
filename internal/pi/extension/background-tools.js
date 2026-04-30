import http from "node:http";
import { Type } from "@sinclair/typebox";

const StartParams = Type.Object({
  command: Type.String({ minLength: 1 }),
  cwd: Type.Optional(Type.String()),
  title: Type.Optional(Type.String()),
  env: Type.Optional(Type.Record(Type.String(), Type.String())),
  readyPattern: Type.Optional(Type.String()),
  readyUrl: Type.Optional(Type.String()),
  readyPort: Type.Optional(Type.Integer({ minimum: 1, maximum: 65535 })),
  readyTimeoutMs: Type.Optional(Type.Integer({ minimum: 100, maximum: 120000 })),
  outputTailBytes: Type.Optional(Type.Integer({ minimum: 0, maximum: 262144 })),
});

const OutputParams = Type.Object({
  jobId: Type.String({ minLength: 1 }),
  sinceSeq: Type.Optional(Type.Integer({ minimum: 0 })),
  tailBytes: Type.Optional(Type.Integer({ minimum: 0, maximum: 262144 })),
  tailLines: Type.Optional(Type.Integer({ minimum: 1, maximum: 2000 })),
});

const WaitParams = Type.Object({
  jobId: Type.String({ minLength: 1 }),
  condition: Type.Optional(Type.String()),
  pattern: Type.Optional(Type.String()),
  timeoutMs: Type.Optional(Type.Integer({ minimum: 100, maximum: 120000 })),
  sinceSeq: Type.Optional(Type.Integer({ minimum: 0 })),
});

const SendParams = Type.Object({
  jobId: Type.String({ minLength: 1 }),
  input: Type.String(),
  appendNewline: Type.Optional(Type.Boolean()),
});

const KillParams = Type.Object({
  jobId: Type.String({ minLength: 1 }),
  signal: Type.Optional(Type.Union([
    Type.Literal("SIGTERM"),
    Type.Literal("SIGINT"),
    Type.Literal("SIGHUP"),
    Type.Literal("SIGKILL"),
  ])),
  reason: Type.Optional(Type.String()),
});

const ListParams = Type.Object({
  status: Type.Optional(Type.String()),
});

const ShellExecParams = Type.Object({
  command: Type.String({ minLength: 1 }),
  cwd: Type.Optional(Type.String()),
  timeoutMs: Type.Optional(Type.Integer({ minimum: 1000, maximum: 120000 })),
  mode: Type.Optional(Type.Union([
    Type.Literal("auto"),
    Type.Literal("foreground"),
    Type.Literal("background"),
  ])),
});

export async function callAgentTools(path, body, { signal, env = process.env } = {}) {
  const socketPath = env.GSD_AGENT_TOOLS_SOCKET;
  const token = env.GSD_AGENT_TOOLS_TOKEN;
  if (!socketPath || !token) {
    throw new Error("Agent terminal tools are unavailable for this task.");
  }
  const payload = JSON.stringify(body ?? {});
  return await new Promise((resolve, reject) => {
    const req = http.request({
      socketPath,
      path,
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
        "Content-Length": Buffer.byteLength(payload),
      },
      signal,
    }, (res) => {
      const chunks = [];
      res.on("data", (chunk) => chunks.push(Buffer.from(chunk)));
      res.on("end", () => {
        const text = Buffer.concat(chunks).toString("utf8");
        let json = {};
        if (text.trim()) {
          try {
            json = JSON.parse(text);
          } catch (err) {
            reject(err);
            return;
          }
        }
        if (!res.statusCode || res.statusCode < 200 || res.statusCode >= 300 || json.error) {
          reject(new Error(json.error ?? `agent terminal request failed with ${res.statusCode}`));
          return;
        }
        resolve(json);
      });
    });
    req.on("error", reject);
    req.write(payload);
    req.end();
  });
}

function toolResult(json) {
  return {
    content: [{ type: "text", text: JSON.stringify(json) }],
    isError: false,
    details: json,
  };
}

function toolError(err) {
  return {
    content: [{ type: "text", text: err instanceof Error ? err.message : String(err) }],
    isError: true,
    details: {},
  };
}

function makeTool(name, label, description, parameters, path) {
  return {
    name,
    label,
    description,
    parameters,
    async execute(_toolCallId, params, signal) {
      try {
        return toolResult(await callAgentTools(path, params, { signal }));
      } catch (err) {
        return toolError(err);
      }
    },
  };
}

export const backgroundTools = [
  makeTool("background_start", "Start background command", "Start a PTY-backed background command and return a job id.", StartParams, "/background/start"),
  makeTool("background_output", "Read background output", "Read bounded output from a background job.", OutputParams, "/background/output"),
  makeTool("background_wait", "Wait for background job", "Wait for output, readiness, or exit from a background job.", WaitParams, "/background/wait"),
  makeTool("background_send", "Send background input", "Send input to an interactive background job.", SendParams, "/background/send"),
  makeTool("background_kill", "Kill background job", "Stop a background job.", KillParams, "/background/kill"),
  makeTool("background_list", "List background jobs", "List active background jobs for this session.", ListParams, "/background/list"),
  makeTool("shell_exec", "Run shell command", "Run a finite shell command or route long-running commands to a background job.", ShellExecParams, "/shell/exec"),
];

