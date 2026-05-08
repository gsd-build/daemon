#!/usr/bin/env node
// bench-pi-runtime.mjs - Compare stock Pi and pi-rust through the daemon lab boundary.
// Run when changing daemon Pi runtime behavior or deciding whether pi-rust improves GSD latency.
// Exit code is non-zero when a benchmark variant fails to complete all measured tasks.

import { spawn, spawnSync } from "node:child_process";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import path from "node:path";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const daemonDirDefault = path.resolve(scriptDir, "..");

function usage() {
  return `Usage:
  scripts/bench-pi-runtime.mjs [options]

Options:
  --daemon-dir PATH           Daemon repo path. Default: this repo.
  --cwd PATH                  Task working directory. Default: current directory.
  --stock-pi PATH             Stock Pi binary. Default: command -v pi.
  --rust-pi PATH              pi-rust binary. Default: ~/Developer/pi/pi-rust/target/release/pi-rust.
  --stock-npm-prefix PATH     Prefix used by the stock Pi wrapper for package installs.
  --no-stock-wrapper          Run stock Pi directly.
  --provider VALUE            Provider for both runtimes. Default: openrouter.
  --model VALUE               Model for both runtimes. Default: z-ai/glm-4.7-flash.
  --text TEXT                 Prompt text. Default: exact reply benchmark prompt.
  --mode cold|warm|both       Warm-worker config to measure. Default: cold.
  --iterations N              Measured tasks per variant. Default: 5.
  --warmup N                  Unmeasured tasks before each variant. Default: 0 cold, 1 warm.
  --effort VALUE              Reasoning effort. Default: low.
  --permission-mode VALUE     Permission mode. Default: acceptEdits.
  --timeout-ms N              Per-task timeout. Default: 180000.
  --startup-timeout-ms N      Lab startup timeout. Default: 90000.
  --output PATH               Write full JSON result. Default: /tmp/gsd-pi-runtime-bench-*.json.
  --json                      Print full JSON result.
  --jsonl                     Print one compact JSON object per variant.
`;
}

function parseArgs(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i += 1) {
    const raw = argv[i];
    if (!raw.startsWith("--")) throw new Error(`Unexpected positional argument: ${raw}`);
    const eq = raw.indexOf("=");
    if (eq !== -1) {
      args[raw.slice(2, eq)] = raw.slice(eq + 1);
      continue;
    }
    const key = raw.slice(2);
    const next = argv[i + 1];
    if (!next || next.startsWith("--")) {
      args[key] = true;
      continue;
    }
    args[key] = next;
    i += 1;
  }
  return args;
}

function intArg(args, key, fallback) {
  if (args[key] === undefined) return fallback;
  const value = Number(args[key]);
  if (!Number.isInteger(value) || value < 0) throw new Error(`--${key} must be a non-negative integer`);
  return value;
}

function stringArg(args, key, fallback) {
  return typeof args[key] === "string" && args[key].trim() !== "" ? args[key].trim() : fallback;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function commandOutput(command) {
  const result = spawnSync("sh", ["-lc", command], { encoding: "utf8" });
  if (result.status !== 0) return "";
  return result.stdout.trim();
}

function expandHome(value) {
  if (!value.startsWith("~/")) return value;
  return path.join(homedir(), value.slice(2));
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", "'\\''")}'`;
}

async function createStockWrapper(stockPi, npmPrefix) {
  const dir = await mkdtemp(path.join(tmpdir(), "gsd-stock-pi-wrapper-"));
  const wrapper = path.join(dir, "pi-stock-wrapper");
  const script = [
    "#!/bin/sh",
    `export NPM_CONFIG_PREFIX=${shellQuote(npmPrefix)}`,
    `exec ${shellQuote(stockPi)} "$@"`,
    "",
  ].join("\n");
  await writeFile(wrapper, script, { mode: 0o755 });
  return wrapper;
}

function waitForLabURL(child, timeoutMs) {
  let buffer = "";
  let settled = false;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      reject(new Error(`Timed out waiting for daemon lab URL after ${timeoutMs}ms\n${buffer}`));
    }, timeoutMs);
    const onData = (chunk) => {
      buffer += chunk.toString("utf8");
      const match = buffer.match(/provider lab:\s+(http:\/\/[^\s]+)/);
      if (match && !settled) {
        settled = true;
        clearTimeout(timer);
        resolve({ url: match[1], output: buffer });
      }
    };
    child.stdout.on("data", onData);
    child.stderr.on("data", onData);
    child.on("exit", (code, signal) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(new Error(`daemon lab exited before URL: code=${code} signal=${signal}\n${buffer}`));
    });
    child.on("error", (error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(error);
    });
  });
}

async function fetchJSON(url) {
  const response = await fetch(url);
  const text = await response.text();
  if (!response.ok) throw new Error(`HTTP ${response.status} from ${url}: ${text}`);
  return JSON.parse(text);
}

async function waitForDaemonConnection(baseURL, timeoutMs) {
  const startedAt = performance.now();
  while (performance.now() - startedAt < timeoutMs) {
    const bundle = await fetchJSON(`${baseURL}/api/export`);
    const connected = (bundle.events ?? []).some((event) => {
      if (event.kind !== "relay.daemon.recv") return false;
      return event.payload?.type === "hello" || JSON.stringify(event.payload ?? {}).includes('"hello"');
    });
    if (connected) return;
    await sleep(100);
  }
  throw new Error(`Timed out waiting for local daemon connection after ${timeoutMs}ms`);
}

function connectControlSocket(baseURL) {
  const wsURL = baseURL.replace(/^http:/, "ws:") + "/ws/ui";
  const ws = new WebSocket(wsURL);
  const records = [];
  ws.addEventListener("message", (message) => {
    const receivedAtMs = performance.now();
    try {
      records.push({ receivedAtMs, event: JSON.parse(message.data) });
    } catch {
      records.push({ receivedAtMs, event: { type: "parse_error", raw: String(message.data) } });
    }
  });
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`Timed out connecting to ${wsURL}`)), 10_000);
    ws.addEventListener("open", () => {
      clearTimeout(timer);
      resolve({ ws, records, wsURL });
    });
    ws.addEventListener("error", () => {
      clearTimeout(timer);
      reject(new Error(`WebSocket error connecting to ${wsURL}`));
    });
  });
}

function eventText(event) {
  const stream = event.type === "stream" ? event.event : event.payload?.event;
  if (stream?.type === "assistant") {
    const message = stream.message;
    if (!message || !Array.isArray(message.content)) return "";
    return message.content
      .filter((block) => block?.type === "text" && typeof block.text === "string")
      .map((block) => block.text)
      .join("");
  }
  if (stream?.type === "stream_event") {
    const delta = stream.event?.delta;
    if (delta?.type === "text_delta" && typeof delta.text === "string") return delta.text;
  }
  const message = stream?.message;
  if (!message || message.role !== "assistant" || !Array.isArray(message.content)) return "";
  return message.content
    .filter((block) => block?.type === "text" && typeof block.text === "string")
    .map((block) => block.text)
    .join("");
}

function taskMatches(record, payload) {
  const event = record.event;
  return event.taskId === payload.taskId ||
    event.sessionId === payload.sessionId ||
    event.channelId === payload.channelId ||
    event.event?.taskId === payload.taskId ||
    event.event?.sessionId === payload.sessionId ||
    event.event?.channelId === payload.channelId ||
    event.payload?.taskId === payload.taskId ||
    event.payload?.sessionId === payload.sessionId ||
    event.payload?.channelId === payload.channelId;
}

function taskDone(record, payload) {
  return taskMatches(record, payload) && ["taskComplete", "taskError", "taskCancelled"].includes(record.event.type);
}

async function waitForTask(records, startIndex, payload, timeoutMs) {
	const startedAt = performance.now();
	while (performance.now() - startedAt < timeoutMs) {
		const done = records.slice(startIndex).find((record) => taskDone(record, payload));
		if (done) return { timedOut: false };
		await sleep(50);
	}
  return { timedOut: true };
}

function summarizeTask(records, payload, sentAtMs, timedOut) {
  const taskRecords = records.filter((record) => taskMatches(record, payload));
  const taskStarted = taskRecords.find((record) => record.event.type === "taskStarted") ?? null;
  const taskComplete = taskRecords.find((record) => record.event.type === "taskComplete") ?? null;
  const taskError = taskRecords.find((record) => record.event.type === "taskError") ?? null;
  const taskCancelled = taskRecords.find((record) => record.event.type === "taskCancelled") ?? null;
  const streamRecords = taskRecords.filter((record) => record.event.type === "stream");
  const firstStream = streamRecords[0] ?? null;
  const textRecords = streamRecords.filter((record) => eventText(record.event));
  const firstText = textRecords[0] ?? null;
  const finalAssistantText = textRecords.map((record) => eventText(record.event)).filter(Boolean).at(-1) ?? "";
  const assistantText = finalAssistantText || textRecords.map((record) => eventText(record.event)).filter(Boolean).join("");
  let status = "no_provider_output";
  if (taskComplete && (assistantText || streamRecords.length > 0)) status = "ok";
  else if (taskComplete) status = "task_complete_no_output";
  else if (taskError) status = "task_error";
  else if (taskCancelled) status = "task_cancelled";
  else if (timedOut) status = taskStarted ? "timeout_after_start" : "timeout_no_start";
  else if (!taskStarted) status = "no_task_start";
  const done = taskComplete ?? taskError ?? taskCancelled ?? null;
  return {
    status,
    taskStartedMs: taskStarted ? Math.round(taskStarted.receivedAtMs - sentAtMs) : null,
    firstStreamMs: firstStream ? Math.round(firstStream.receivedAtMs - sentAtMs) : null,
    firstTextMs: firstText ? Math.round(firstText.receivedAtMs - sentAtMs) : null,
    taskCompleteMs: done ? Math.round(done.receivedAtMs - sentAtMs) : null,
    taskEventCount: taskRecords.length,
    streamEventCount: streamRecords.length,
    assistantText,
    taskError: taskError?.event?.error ?? taskError?.event?.message ?? null,
  };
}

function makePayload(input) {
  return {
    type: "task",
    taskId: `${input.variant}-${input.mode}-${input.index}-${Date.now()}-${Math.random().toString(16).slice(2)}`,
    sessionId: input.sessionId,
    channelId: input.channelId,
    prompt: input.text,
    engine: "pi",
    provider: input.provider,
    model: input.model,
    effort: input.effort,
    permissionMode: input.permissionMode,
    cwd: input.cwd,
  };
}

async function sendTask(control, input) {
	const payload = makePayload(input);
	const startIndex = control.records.length;
	const sentAtMs = performance.now();
	control.ws.send(JSON.stringify(payload));
	const wait = await waitForTask(control.records, startIndex, payload, input.timeoutMs);
	const records = control.records.slice(startIndex);
  return {
    index: input.index,
    included: input.included,
    ids: {
      sessionId: payload.sessionId,
      taskId: payload.taskId,
      channelId: payload.channelId,
    },
    timedOut: wait.timedOut,
    ...summarizeTask(records, payload, sentAtMs, wait.timedOut),
  };
}

function stat(values) {
  const sorted = values.filter((value) => Number.isFinite(value)).sort((a, b) => a - b);
  if (sorted.length === 0) return null;
  const percentile = (p) => sorted[Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1)];
  return {
    n: sorted.length,
    min: sorted[0],
    p50: percentile(50),
    p95: percentile(95),
    max: sorted[sorted.length - 1],
    mean: Math.round(sorted.reduce((sum, value) => sum + value, 0) / sorted.length),
  };
}

function summarizeRuns(runs) {
  const included = runs.filter((run) => run.included);
  return {
    ok: included.every((run) => run.status === "ok"),
    measuredRuns: included.length,
    failures: included.filter((run) => run.status !== "ok").map((run) => ({
      index: run.index,
      status: run.status,
      taskError: run.taskError,
    })),
    taskStartedMs: stat(included.map((run) => run.taskStartedMs)),
    firstStreamMs: stat(included.map((run) => run.firstStreamMs)),
    firstTextMs: stat(included.map((run) => run.firstTextMs)),
    taskCompleteMs: stat(included.map((run) => run.taskCompleteMs)),
  };
}

async function runVariant(options) {
  const labArgs = [
    "run", ".", "lab",
    "--cwd", options.cwd,
    "--provider", options.provider,
    "--model", options.model,
    "--effort", options.effort,
    "--permission-mode", options.permissionMode,
    "--port", "0",
    "--pi-binary", options.piBinary,
    "--warm-workers", options.warmWorkers ? "on" : "off",
  ];
  const child = spawn("go", labArgs, {
    cwd: options.daemonDir,
    env: process.env,
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const labOutput = [];
  child.stdout.on("data", (chunk) => labOutput.push(chunk.toString("utf8")));
  child.stderr.on("data", (chunk) => labOutput.push(chunk.toString("utf8")));

  try {
    const lab = await waitForLabURL(child, options.startupTimeoutMs);
    const labURL = lab.url;
    await waitForDaemonConnection(labURL, 15_000);
    const control = await connectControlSocket(labURL);
    const sessionId = `${options.variant}-${options.mode}-${Date.now()}`;
    const channelId = `${sessionId}-channel`;
    const runs = [];
    const totalRuns = options.warmup + options.iterations;
    for (let index = 0; index < totalRuns; index += 1) {
      runs.push(await sendTask(control, {
        variant: options.variant,
        mode: options.mode,
        index,
        included: index >= options.warmup,
        sessionId,
        channelId,
        text: options.text,
        provider: options.provider,
        model: options.model,
        effort: options.effort,
        permissionMode: options.permissionMode,
        cwd: options.cwd,
        timeoutMs: options.timeoutMs,
      }));
    }
    control.ws.close();
    const bundle = await fetchJSON(`${labURL}/api/export`);
    return {
      variant: options.variant,
      mode: options.mode,
      piBinary: options.piBinary,
      warmWorkers: options.warmWorkers,
      warmup: options.warmup,
      iterations: options.iterations,
      labURL,
      wsURL: control.wsURL,
      runs,
      summary: summarizeRuns(runs),
      bundle,
      labOutput: labOutput.join(""),
    };
  } finally {
    if (child.pid) {
      try {
        process.kill(-child.pid, "SIGTERM");
      } catch {}
      setTimeout(() => {
        try {
          process.kill(-child.pid, "SIGKILL");
        } catch {}
      }, 2_000).unref();
    }
  }
}

function compareMode(results, mode) {
  const stock = results.find((result) => result.variant === "stock" && result.mode === mode);
  const rust = results.find((result) => result.variant === "rust" && result.mode === mode);
  if (!stock || !rust) return null;
  const ratio = (metric) => {
    const stockP50 = stock.summary[metric]?.p50;
    const rustP50 = rust.summary[metric]?.p50;
    if (!stockP50 || !rustP50) return null;
    return Number((stockP50 / rustP50).toFixed(2));
  };
	return {
		mode,
		firstStreamP50Speedup: ratio("firstStreamMs"),
		firstTextP50Speedup: ratio("firstTextMs"),
		taskCompleteP50Speedup: ratio("taskCompleteMs"),
		stock: {
			firstStreamMs: stock.summary.firstStreamMs,
			firstTextMs: stock.summary.firstTextMs,
			taskCompleteMs: stock.summary.taskCompleteMs,
		},
		rust: {
			firstStreamMs: rust.summary.firstStreamMs,
			firstTextMs: rust.summary.firstTextMs,
			taskCompleteMs: rust.summary.taskCompleteMs,
		},
  };
}

function compactVariant(result) {
  return {
    variant: result.variant,
    mode: result.mode,
    ok: result.summary.ok,
		measuredRuns: result.summary.measuredRuns,
		firstStreamMs: result.summary.firstStreamMs,
		firstTextMs: result.summary.firstTextMs,
		taskCompleteMs: result.summary.taskCompleteMs,
    failures: result.summary.failures,
    piBinary: result.piBinary,
    warmWorkers: result.warmWorkers,
  };
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    console.log(usage());
    return;
  }

  const daemonDir = path.resolve(stringArg(args, "daemon-dir", daemonDirDefault));
  const cwd = path.resolve(stringArg(args, "cwd", process.cwd()));
  const stockPi = path.resolve(expandHome(stringArg(args, "stock-pi", commandOutput("command -v pi"))));
  const rustPi = path.resolve(expandHome(stringArg(args, "rust-pi", "~/Developer/pi/pi-rust/target/release/pi-rust")));
  const stockNpmPrefix = path.resolve(expandHome(stringArg(args, "stock-npm-prefix", path.dirname(path.dirname(stockPi)))));
  const stockPiForDaemon = args["no-stock-wrapper"] ? stockPi : await createStockWrapper(stockPi, stockNpmPrefix);
  const provider = stringArg(args, "provider", "openrouter");
  const model = stringArg(args, "model", "z-ai/glm-4.7-flash");
  const text = stringArg(args, "text", "Reply exactly: gsd-pi-runtime-benchmark-ok");
  const mode = stringArg(args, "mode", "cold");
  if (!["cold", "warm", "both"].includes(mode)) throw new Error("--mode must be cold, warm, or both");
  const modes = mode === "both" ? ["cold", "warm"] : [mode];
  const iterations = intArg(args, "iterations", 5);
  const effort = stringArg(args, "effort", "low");
  const permissionMode = stringArg(args, "permission-mode", "acceptEdits");
  const timeoutMs = intArg(args, "timeout-ms", 180_000);
  const startupTimeoutMs = intArg(args, "startup-timeout-ms", 90_000);
  const startedAt = new Date().toISOString();
  const results = [];

  for (const currentMode of modes) {
    const warmWorkers = currentMode === "warm";
    const warmup = intArg(args, "warmup", warmWorkers ? 1 : 0);
    for (const variant of [
      { name: "stock", piBinary: stockPiForDaemon },
      { name: "rust", piBinary: rustPi },
    ]) {
      results.push(await runVariant({
        daemonDir,
        cwd,
        variant: variant.name,
        piBinary: variant.piBinary,
        mode: currentMode,
        warmWorkers,
        warmup,
        iterations,
        provider,
        model,
        text,
        effort,
        permissionMode,
        timeoutMs,
        startupTimeoutMs,
      }));
    }
  }

  const comparisons = modes.map((currentMode) => compareMode(results, currentMode)).filter(Boolean);
  const output = {
    schemaVersion: 1,
    startedAt,
    completedAt: new Date().toISOString(),
    daemonDir,
    cwd,
    provider,
    model,
    text,
    effort,
    permissionMode,
    stockPi,
    stockPiForDaemon,
    stockNpmPrefix,
    rustPi,
    iterations,
    modes,
    results,
    comparisons,
  };
  const outputPath = path.resolve(stringArg(args, "output", path.join(tmpdir(), `gsd-pi-runtime-bench-${Date.now()}.json`)));
  await mkdir(path.dirname(outputPath), { recursive: true });
  await writeFile(outputPath, JSON.stringify(output, null, 2));
  output.outputPath = outputPath;

  if (args.json) {
    console.log(JSON.stringify(output, null, 2));
  } else if (args.jsonl) {
    for (const result of results) console.log(JSON.stringify(compactVariant(result)));
    for (const comparison of comparisons) console.log(JSON.stringify({ comparison }));
    console.log(JSON.stringify({ outputPath }));
  } else {
    for (const result of results) {
			const complete = result.summary.taskCompleteMs;
			const firstStream = result.summary.firstStreamMs;
			const firstText = result.summary.firstTextMs;
			console.log([
				`${result.variant}/${result.mode}`,
				`ok=${result.summary.ok}`,
				`n=${result.summary.measuredRuns}`,
				`firstStream.p50=${firstStream?.p50 ?? "n/a"}ms`,
				`firstText.p50=${firstText?.p50 ?? "n/a"}ms`,
				`complete.p50=${complete?.p50 ?? "n/a"}ms`,
				`complete.p95=${complete?.p95 ?? "n/a"}ms`,
      ].join(" "));
    }
    for (const comparison of comparisons) {
			console.log([
				`comparison/${comparison.mode}`,
				`firstStreamSpeedup=${comparison.firstStreamP50Speedup ?? "n/a"}x`,
				`firstTextSpeedup=${comparison.firstTextP50Speedup ?? "n/a"}x`,
				`completeSpeedup=${comparison.taskCompleteP50Speedup ?? "n/a"}x`,
			].join(" "));
    }
    console.log(`outputPath=${outputPath}`);
  }

  if (results.some((result) => !result.summary.ok)) process.exitCode = 2;
}

main().catch((error) => {
  console.error(error.stack || error.message);
  process.exitCode = 1;
});
