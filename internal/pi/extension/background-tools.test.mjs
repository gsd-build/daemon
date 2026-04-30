import assert from "node:assert/strict";
import http from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { backgroundTools, callAgentTools } from "./background-tools.js";
import { schemaToZod } from "./schema-to-zod.js";

const byName = new Map(backgroundTools.map((tool) => [tool.name, tool]));

test("background_start schema accepts command", () => {
  const schema = schemaToZod(byName.get("background_start").parameters);
  schema.parse({ command: "pnpm dev", cwd: "/tmp/project" });
});

test("background_start schema rejects missing command", () => {
  const schema = schemaToZod(byName.get("background_start").parameters);
  assert.throws(() => schema.parse({ cwd: "/tmp/project" }));
});

test("local client sends bearer token", async () => {
  const dir = mkdtempSync(join(tmpdir(), "gsd-bg-tools-"));
  const socketPath = join(dir, "tools.sock");
  let authorization = "";
  const server = http.createServer((req, res) => {
    authorization = req.headers.authorization ?? "";
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ ok: true }));
  });
  await new Promise((resolve) => server.listen(socketPath, resolve));
  try {
    const result = await callAgentTools("/background/list", {}, {
      env: { GSD_AGENT_TOOLS_SOCKET: socketPath, GSD_AGENT_TOOLS_TOKEN: "token-1" },
    });
    assert.deepEqual(result, { ok: true });
    assert.equal(authorization, "Bearer token-1");
  } finally {
    await new Promise((resolve) => server.close(resolve));
    rmSync(dir, { recursive: true, force: true });
  }
});

test("local client throws JSON error responses", async () => {
  const dir = mkdtempSync(join(tmpdir(), "gsd-bg-tools-"));
  const socketPath = join(dir, "tools.sock");
  const server = http.createServer((_req, res) => {
    res.statusCode = 400;
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ error: "bad command" }));
  });
  await new Promise((resolve) => server.listen(socketPath, resolve));
  try {
    await assert.rejects(
      callAgentTools("/shell/exec", { command: "pnpm dev" }, {
        env: { GSD_AGENT_TOOLS_SOCKET: socketPath, GSD_AGENT_TOOLS_TOKEN: "token-1" },
      }),
      /bad command/,
    );
  } finally {
    await new Promise((resolve) => server.close(resolve));
    rmSync(dir, { recursive: true, force: true });
  }
});

test("shell_exec schema accepts supported modes", () => {
  const schema = schemaToZod(byName.get("shell_exec").parameters);
  for (const mode of ["auto", "foreground", "background"]) {
    schema.parse({ command: "printf ok", mode });
  }
});

test("tool execute forwards tool call id", async () => {
  const dir = mkdtempSync(join(tmpdir(), "gsd-bg-tools-"));
  const socketPath = join(dir, "tools.sock");
  let body = {};
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on("data", (chunk) => chunks.push(Buffer.from(chunk)));
    req.on("end", () => {
      body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ ok: true }));
    });
  });
  await new Promise((resolve) => server.listen(socketPath, resolve));
  const previousSocket = process.env.GSD_AGENT_TOOLS_SOCKET;
  const previousToken = process.env.GSD_AGENT_TOOLS_TOKEN;
  process.env.GSD_AGENT_TOOLS_SOCKET = socketPath;
  process.env.GSD_AGENT_TOOLS_TOKEN = "token-1";
  try {
    const result = await byName.get("background_list").execute("toolu_1", {}, undefined);
    assert.equal(result.isError, false);
    assert.equal(body.toolCallId, "toolu_1");
  } finally {
    if (previousSocket === undefined) {
      delete process.env.GSD_AGENT_TOOLS_SOCKET;
    } else {
      process.env.GSD_AGENT_TOOLS_SOCKET = previousSocket;
    }
    if (previousToken === undefined) {
      delete process.env.GSD_AGENT_TOOLS_TOKEN;
    } else {
      process.env.GSD_AGENT_TOOLS_TOKEN = previousToken;
    }
    await new Promise((resolve) => server.close(resolve));
    rmSync(dir, { recursive: true, force: true });
  }
});
