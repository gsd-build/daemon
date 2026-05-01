import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const extensionPath = path.join(__dirname, "index.ts");
const model = process.argv[2] || "gpt-5.5";

const child = spawn("pi", [
  "-p",
  "--mode",
  "rpc",
  "-e",
  extensionPath,
  "--provider",
  "codex-appserver",
  "--model",
  model,
  "--no-extensions",
  "--no-skills",
  "--no-prompt-templates",
  "--offline",
  "--no-session",
], {
  cwd: process.cwd(),
  stdio: ["pipe", "pipe", "pipe"],
});

const rl = createInterface({ input: child.stdout });
let stderr = "";
child.stderr.on("data", (chunk) => {
  stderr += chunk.toString();
});
let sawAgentEnd = false;
const flags = {
  sawPiToolCall: false,
  sawToolStart: false,
  sawToolEnd: false,
  sawUiRequest: false,
  sawUiResponse: false,
  finalContainsAnswer: false,
};
let pendingUiRequestId = null;

rl.on("line", (line) => {
  if (!line.trim()) return;
  let event;
  try {
    event = JSON.parse(line);
  } catch (error) {
    throw new Error(`invalid pi event: ${line}\n${error instanceof Error ? error.message : String(error)}`);
  }
  if (event.type === "message_update" && JSON.stringify(event).includes("ask_user_questions")) {
    flags.sawPiToolCall = true;
  }
  const toolName = event.toolName || event.tool_name;
  if (event.type === "tool_execution_start" && toolName === "ask_user_questions") {
    flags.sawToolStart = true;
  }
  if (event.type === "tool_execution_end" && toolName === "ask_user_questions") {
    flags.sawToolEnd = true;
  }
  if (event.type === "extension_ui_request") {
    flags.sawUiRequest = true;
    pendingUiRequestId = event.id;
    child.stdin.write(`${JSON.stringify({
      type: "extension_ui_response",
      id: pendingUiRequestId,
      result: JSON.stringify({ answers: [{ id: "priority", answer: "speed" }] }),
    })}\n`);
    flags.sawUiResponse = true;
  }
  if (event.type === "agent_end") {
    sawAgentEnd = true;
    if (JSON.stringify(event).includes("speed")) {
      flags.finalContainsAnswer = true;
    }
    child.kill("SIGTERM");
  }
});

child.stdin.write(`${JSON.stringify({
  id: "codex-smoke-prompt",
  type: "prompt",
  message: "Ask Lex whether to prioritize speed or quality using ask_user_questions. After the answer, reply with Codex saw: <answer>.",
})}\n`);

const result = await new Promise((resolve, reject) => {
  const timeout = setTimeout(() => {
    child.kill("SIGTERM");
    reject(new Error(`pi smoke timed out\n${stderr}`));
  }, 180_000);
  child.on("error", reject);
  child.on("close", (code, signal) => {
    clearTimeout(timeout);
    resolve({ code, signal });
  });
});
if (result.code !== 0 && !(sawAgentEnd && (result.signal === "SIGTERM" || result.code === 143))) {
  throw new Error(`pi exited with ${result.code ?? result.signal}\n${stderr}`);
}
if (!sawAgentEnd) {
  throw new Error("missing smoke flag sawAgentEnd");
}
console.log(JSON.stringify({ model, flags }, null, 2));
