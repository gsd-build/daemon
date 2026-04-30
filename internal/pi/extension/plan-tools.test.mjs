import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { compactProjectState, hasPlanCapability, registerPlanTools } from "./plan-tools.js";
import { schemaToZod } from "./schema-to-zod.js";

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
      "plan_check_criterion",
      "plan_check_sub_task",
      "plan_commit",
      "plan_create",
      "plan_get_archived_plan",
      "plan_get_project_state",
      "plan_pause",
      "plan_rename",
      "plan_reorder_items",
      "plan_resume",
      "plan_update_agent_criteria",
      "plan_update_item",
      "plan_update_sub_tasks",
      "plan_update_user_context",
    ].sort(),
  );
  const names = tools.map((tool) => tool.name);
  assert.ok(names.indexOf("plan_commit") < names.indexOf("plan_create"));
});

test("plan_commit validates multi-operation payloads", () => {
  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const commit = tools.find((tool) => tool.name === "plan_commit");
  assert.ok(commit);

  const parsed = schemaToZod(commit.parameters).parse({
    mutationId: "mut_commit",
    ops: [
      { type: "start_next_item", leaseTtlSeconds: 120 },
      {
        type: "set_execution_contract",
        itemId: "item-1",
        subTasks: [{ text: "Implement" }],
        agentCriteria: [{ text: "Tests pass" }],
      },
    ],
  });

  assert.equal(parsed.ops.length, 2);
});

test("plan_commit rejects stringified arrays before network forwarding", async () => {
  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const commit = tools.find((tool) => tool.name === "plan_commit");
  assert.ok(commit);

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    return Response.json({ ok: true });
  };
  try {
    const result = await commit.execute("toolu_commit", {
      mutationId: "mut_bad",
      ops: [
        {
          type: "set_execution_contract",
          itemId: "item-1",
          subTasks: JSON.stringify([{ text: "Do not stringify" }]),
          agentCriteria: [{ text: "Arrays stay arrays" }],
        },
      ],
    });
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /invalid_arguments/);
    assert.equal(result.details.error.code, "invalid_arguments");
    assert.equal(result.details.error.retryable, false);
    assert.equal(result.details.error.fieldErrors[0].path, "ops.0.subTasks");
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(calls.length, 0);
});

test("plan_commit reports selected operation field errors without union noise", async () => {
  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const commit = tools.find((tool) => tool.name === "plan_commit");
  assert.ok(commit);

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    return Response.json({ ok: true });
  };
  try {
    const result = await commit.execute("toolu_commit", {
      mutationId: "mut_bad_ttl",
      ops: [{ type: "start_next_item", leaseTtlSeconds: 1800 }],
    });

    assert.equal(result.isError, true);
    assert.equal(result.details.error.code, "invalid_arguments");
    const paths = result.details.error.fieldErrors.map((error) => error.path);
    assert.ok(paths.includes("ops.0.leaseTtlSeconds"));
    assert.ok(!paths.includes("ops.0.title"));
    assert.ok(!paths.includes("ops.0.items"));
    assert.ok(!paths.includes("ops.0.itemId"));
    assert.match(
      result.details.error.fieldErrors.find((error) => error.path === "ops.0.leaseTtlSeconds").message,
      /900|less than or equal/,
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(calls.length, 0);
});

test("plan_commit rejects human-readable evidence refs before network forwarding", async () => {
  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const commit = tools.find((tool) => tool.name === "plan_commit");
  assert.ok(commit);

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    return Response.json({ ok: true });
  };
  try {
    const result = await commit.execute("toolu_commit", {
      mutationId: "mut_bad_evidence",
      ops: [
        {
          type: "complete_item",
          itemId: "22222222-2222-4222-8222-222222222222",
          result: {
            summary: "Done",
            evidenceRefs: ["main.py:23"],
          },
        },
      ],
    });

    assert.equal(result.isError, true);
    assert.equal(result.details.error.code, "invalid_arguments");
    assert.deepEqual(
      result.details.error.fieldErrors.map((error) => error.path),
      ["ops.0.result.evidenceRefs.0"],
    );
    assert.match(result.details.error.fieldErrors[0].message, /Evidence refs must be evidence record IDs/);
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(calls.length, 0);
});

test("plan_commit forwards runtime id and defaults response detail", async () => {
  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const commit = tools.find((tool) => tool.name === "plan_commit");
  assert.ok(commit);

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    return Response.json({ ok: true, commandId: "command-1" });
  };
  try {
    const result = await commit.execute("toolu_commit", {
      mutationId: "mut_commit",
      ops: [{ type: "start_next_item" }],
    });
    assert.equal(result.isError, false);
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(calls[0].url, "https://app.test/api/agent-plan/commit");
  assert.deepEqual(JSON.parse(calls[0].options.body), {
    mutationId: "mut_commit",
    toolCallId: "toolu_commit",
    ops: [{ type: "start_next_item" }],
    responseDetail: "execution_packet",
  });
});

test("plan_commit cloud errors complete as errored tool results", async () => {
  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const commit = tools.find((tool) => tool.name === "plan_commit");
  assert.ok(commit);

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () =>
    Response.json({
      ok: false,
      error: { code: "lease_conflict", message: "Lease expired", retryable: true },
    });
  try {
    const result = await commit.execute("toolu_commit", {
      mutationId: "mut_commit",
      ops: [{ type: "start_next_item" }],
    });
    assert.equal(result.isError, true);
    assert.deepEqual(result.details.error, {
      code: "lease_conflict",
      message: "Lease expired",
      retryable: true,
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
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

const fullProjectState = {
  snapshot: {
    projectId: "project_1",
    planModeEnabled: true,
    revision: 24,
    activePlan: {
      id: "plan_1",
      projectId: "project_1",
      title: "Ship the thing",
      status: "active",
      revision: 3,
      userContext: "commit after each item",
      agentNotes: "watch the UI",
      progress: { completed: 1, totalNonCancelled: 3, cancelled: 0, blocked: 0, percent: 33 },
      currentItemId: null,
      items: [
        {
          id: "item_done",
          title: "Scaffold",
          description: "Long completed description that should not be in the item summary.",
          result: "Completed result",
          status: "completed",
          position: 0,
          completedAt: "2026-04-29T14:58:41.097Z",
          updatedAt: "2026-04-29T14:58:41.097Z",
        },
        {
          id: "item_next",
          title: "Build vocabularies",
          description: "Create the deterministic press data vocabulary module.",
          userContext: "Use stable seeded picks.",
          agentNotes: "",
          result: "",
          status: "pending",
          position: 1,
          updatedAt: "2026-04-29T14:54:26.104Z",
        },
        {
          id: "item_later",
          title: "Render outlet",
          description: "Long future description that should not be in the item summary.",
          status: "pending",
          position: 2,
        },
      ],
    },
    pausedPlans: [],
    archivedPlans: [
      {
        id: "archived_1",
        title: "Archived plan",
        status: "archived",
        progress: { completed: 9, totalNonCancelled: 9, cancelled: 0, blocked: 0, percent: 100 },
        items: [
          {
            id: "archived_item",
            title: "Old item",
            description: "Archived item body should not be returned by compact state.",
            status: "completed",
            position: 0,
          },
        ],
        updatedAt: "2026-04-29T14:49:28.037Z",
        archivedAt: "2026-04-29T14:49:27.968Z",
      },
    ],
    supportedActions: ["plan_get_project_state", "plan_get_archived_plan"],
    updatedAt: "2026-04-29T15:53:27.226Z",
  },
};

test("compactProjectState keeps the actionable item detail and summarizes plan lists", () => {
  const compact = compactProjectState(fullProjectState);
  const active = compact.snapshot.activePlan;

  assert.equal(compact.detail, "compact");
  assert.equal(active.nextItem.id, "item_next");
  assert.equal(active.nextItem.description, "Create the deterministic press data vocabulary module.");
  assert.equal(active.items.length, 3);
  assert.equal(active.items[0].description, undefined);
  assert.equal(active.items[2].description, undefined);
  assert.equal(compact.snapshot.archivedPlans.length, 1);
  assert.equal(compact.snapshot.archivedPlans[0].items, undefined);
  assert.match(compact.fullDetail, /detail":"full"/);
});

test("compactProjectState treats malformed plan lists as empty", () => {
  const compact = compactProjectState({
    snapshot: {
      projectId: "project_1",
      activePlan: {
        id: "plan_1",
        title: "Malformed plan",
        status: "active",
        items: { id: "not_an_array" },
      },
      pausedPlans: { id: "not_an_array" },
      archivedPlans: "not_an_array",
    },
  });

  assert.deepEqual(compact.snapshot.activePlan.items, []);
  assert.equal(compact.snapshot.activePlan.currentItem, null);
  assert.equal(compact.snapshot.activePlan.nextItem, null);
  assert.deepEqual(compact.snapshot.activePlan.blockedItems, []);
  assert.deepEqual(compact.snapshot.pausedPlans, []);
  assert.deepEqual(compact.snapshot.archivedPlans, []);
});

test("compactProjectState treats malformed active plan as absent", () => {
  const compact = compactProjectState({
    snapshot: {
      projectId: "project_1",
      activePlan: "not_a_plan",
      pausedPlans: [],
      archivedPlans: [],
    },
  });

  assert.equal(compact.snapshot.activePlan, null);
});

test("compactProjectState returns malformed snapshot unchanged", () => {
  const malformed = { snapshot: "not_a_snapshot" };
  assert.equal(compactProjectState(malformed), malformed);
});

test("compactProjectState skips malformed entries inside plan lists", () => {
  const compact = compactProjectState({
    snapshot: {
      projectId: "project_1",
      activePlan: {
        id: "plan_1",
        title: "Plan with malformed entries",
        status: "active",
        items: [
          null,
          "not_an_item",
          ["not_an_item"],
          { id: "item_2", title: "Second", status: "pending", position: 2 },
          { id: "item_1", title: "First", status: "pending", position: 1 },
        ],
      },
      pausedPlans: [null, "not_a_plan", { id: "paused_1", title: "Paused", status: "paused" }],
      archivedPlans: [false, ["not_a_plan"], { id: "archived_1", title: "Archived", status: "archived" }],
    },
  });

  assert.deepEqual(
    compact.snapshot.activePlan.items.map((item) => item.id),
    ["item_1", "item_2"],
  );
  assert.equal(compact.snapshot.activePlan.nextItem.id, "item_1");
  assert.deepEqual(
    compact.snapshot.pausedPlans.map((plan) => plan.id),
    ["paused_1"],
  );
  assert.deepEqual(
    compact.snapshot.archivedPlans.map((plan) => plan.id),
    ["archived_1"],
  );
});

test("plan_get_project_state returns compact state by default and full state on request", async () => {
  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const getState = tools.find((tool) => tool.name === "plan_get_project_state");
  assert.ok(getState);

  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => Response.json(fullProjectState);
  try {
    const compactResult = await getState.execute("toolu_state", {});
    const compact = JSON.parse(compactResult.content[0].text);
    assert.equal(compact.detail, "compact");
    assert.equal(compact.snapshot.activePlan.nextItem.description, "Create the deterministic press data vocabulary module.");
    assert.equal(compact.snapshot.activePlan.items[1].description, undefined);
    assert.equal(compact.snapshot.archivedPlans[0].items, undefined);

    const fullResult = await getState.execute("toolu_state", { detail: "full" });
    const full = JSON.parse(fullResult.content[0].text);
    assert.equal(full.snapshot.activePlan.items[1].description, "Create the deterministic press data vocabulary module.");
    assert.equal(full.snapshot.archivedPlans[0].items[0].description, "Archived item body should not be returned by compact state.");
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("plan_update_item completion appends derived filesChanged", async () => {
  const repo = mkdtempSync(join(tmpdir(), "gsd-plan-tools-"));
  execFileSync("git", ["init"], { cwd: repo, stdio: "ignore" });
  execFileSync("git", ["config", "user.email", "test@example.com"], { cwd: repo });
  execFileSync("git", ["config", "user.name", "Test"], { cwd: repo });
  writeFileSync(join(repo, "a.txt"), "one\n");
  execFileSync("git", ["add", "a.txt"], { cwd: repo });
  execFileSync("git", ["commit", "-m", "base"], {
    cwd: repo,
    stdio: "ignore",
    env: {
      ...process.env,
      GIT_AUTHOR_DATE: "2026-04-29T10:00:00Z",
      GIT_COMMITTER_DATE: "2026-04-29T10:00:00Z",
    },
  });
  writeFileSync(join(repo, "a.txt"), "two\n");
  execFileSync("git", ["add", "a.txt"], { cwd: repo });
  execFileSync("git", ["commit", "-m", "change"], {
    cwd: repo,
    stdio: "ignore",
    env: {
      ...process.env,
      GIT_AUTHOR_DATE: "2026-04-29T10:01:00Z",
      GIT_COMMITTER_DATE: "2026-04-29T10:01:00Z",
    },
  });

  const tools = [];
  registerPlanTools({ registerTool: (tool) => tools.push(tool) }, planEnv);
  const update = tools.find((tool) => tool.name === "plan_update_item");
  assert.ok(update);

  const originalFetch = globalThis.fetch;
  const originalCwd = process.cwd();
  const calls = [];
  globalThis.fetch = async (url, options) => {
    calls.push({ url, options });
    if (String(url).endsWith("/project-state")) {
      return Response.json({
        snapshot: {
          activePlan: {
            id: "plan-1",
            items: [{ id: "item-1", startedAt: "2026-04-29T10:00:30Z" }],
          },
        },
      });
    }
    return Response.json({ snapshot: { revision: 2 } });
  };
  try {
    process.chdir(repo);
    const result = await update.execute("toolu_1", {
      planId: "plan-1",
      itemId: "item-1",
      status: "completed",
      result: { summary: "Done", blockers: [] },
      expectedRevision: 1,
      mutationId: "mut_1",
      toolCallId: "toolu_1",
    });
    assert.equal(result.isError, false);
  } finally {
    process.chdir(originalCwd);
    globalThis.fetch = originalFetch;
    rmSync(repo, { recursive: true, force: true });
  }

  const patchCall = calls.find((call) => call.options?.method === "PATCH");
  assert.ok(patchCall);
  assert.deepEqual(JSON.parse(patchCall.options.body).result.filesChanged, ["a.txt"]);
});
