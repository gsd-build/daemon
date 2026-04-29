import assert from "node:assert/strict";
import test from "node:test";
import { hasPlanCapability, registerPlanTools } from "./plan-tools.js";

const planEnv = {
  GSD_PLAN_API_BASE_URL: "https://app.test/",
  GSD_PLAN_CAPABILITY_TOKEN: "gsd_plan_test",
  GSD_PLAN_CAPABILITY_EXPIRES_AT: "2026-04-28T22:30:00Z",
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

test("registerPlanTools registers project state and mutation tools", () => {
  const tools = [];
  const registered = registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  assert.equal(registered, true);
  assert.deepEqual(
    tools.map((tool) => tool.name).sort(),
    [
      "plan_add_item",
      "plan_archive",
      "plan_cancel_item",
      "plan_create",
      "plan_get_archived_plan",
      "plan_get_project_state",
      "plan_pause",
      "plan_rename",
      "plan_reorder_items",
      "plan_resume",
      "plan_update_item",
      "plan_update_user_context",
    ].sort(),
  );
});

test("plan mutation tools call agent plan api with bearer token", async () => {
  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const rename = tools.find((tool) => tool.name === "plan_rename");
  assert.ok(rename);

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    return Response.json({ snapshot: { revision: 2 } });
  };
  try {
    const result = await rename.execute("toolu_1", {
      planId: "plan/one",
      title: "Ship it",
      expectedRevision: 1,
      mutationId: "mut_1",
      toolCallId: "toolu_1",
    });
    assert.equal(result.isError, false);
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, "https://app.test/api/agent-plan/plans/plan%2Fone/rename");
  assert.equal(calls[0].options.method, "PATCH");
  assert.equal(calls[0].options.headers.Authorization, "Bearer gsd_plan_test");
  assert.deepEqual(JSON.parse(calls[0].options.body), {
    planId: "plan/one",
    title: "Ship it",
    expectedRevision: 1,
    mutationId: "mut_1",
    toolCallId: "toolu_1",
  });
});
