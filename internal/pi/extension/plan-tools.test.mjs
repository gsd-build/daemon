import assert from "node:assert/strict";
import test from "node:test";
import { hasPlanCapability, registerPlanTools } from "./plan-tools.js";

const planEnv = {
  GSD_PLAN_API_BASE_URL: "https://app.test/",
  GSD_PLAN_CAPABILITY_ATTEMPT_ID: "attempt_test",
  GSD_PLAN_CAPABILITY_TOKEN: "gsd_plan_test",
  GSD_PLAN_CAPABILITY_EXPIRES_AT: "2999-04-28T22:30:00Z",
};

test("hasPlanCapability requires all env vars", () => {
  assert.equal(hasPlanCapability({}), false);
  assert.equal(hasPlanCapability(planEnv), true);
});

test("registerPlanTools skips registration without capability", () => {
  const tools = [];
  const registered = registerPlanTools({ registerTool: (tool) => tools.push(tool) }, {});
  assert.equal(registered, false);
  assert.equal(tools.length, 0);
});

test("registerPlanTools registers intent tools", () => {
  const tools = [];
  const registered = registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  assert.equal(registered, true);
  assert.deepEqual(
    tools.map((tool) => tool.name),
    [
      "plan_status",
      "plan_next",
      "plan_checkpoint",
      "plan_sync",
      "plan_done",
    ],
  );
});

test("plan_done posts to the intent endpoint and injects toolCallId", async () => {
  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const done = tools.find((tool) => tool.name === "plan_done");
  assert.ok(done);

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    return Response.json({ ok: true, planId: "plan-1", revision: 2, progress: {} });
  };
  try {
    const result = await done.execute("toolu_done", {
      idempotencyKey: "done-1",
      summary: "Complete",
      evidencePolicy: "waive",
      evidenceWaiverReason: "Planning-only work",
    });
    assert.equal(result.isError, false);
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(calls[0].url, "https://app.test/api/agent-plan/intent/plan_done");
  assert.deepEqual(JSON.parse(calls[0].options.body), {
    idempotencyKey: "done-1",
    summary: "Complete",
    evidencePolicy: "waive",
    evidenceWaiverReason: "Planning-only work",
    toolCallId: "toolu_done",
  });
});
