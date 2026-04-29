import { Type } from "@sinclair/typebox";

const MutationMetaParams = {
  expectedRevision: Type.Integer({ minimum: 0 }),
  mutationId: Type.String(),
  toolCallId: Type.String(),
};

const PlanItemStatus = Type.Union([
  Type.Literal("pending"),
  Type.Literal("in_progress"),
  Type.Literal("blocked"),
  Type.Literal("completed"),
  Type.Literal("cancelled"),
]);

export function hasPlanCapability(env = process.env) {
  return Boolean(
    env.GSD_PLAN_API_BASE_URL &&
      env.GSD_PLAN_CAPABILITY_TOKEN &&
      env.GSD_PLAN_CAPABILITY_EXPIRES_AT,
  );
}

function endpoint(path, env = process.env) {
  return `${env.GSD_PLAN_API_BASE_URL.replace(/\/$/, "")}/api/agent-plan${path}`;
}

async function requestPlan(path, { method = "GET", body, signal } = {}, env = process.env) {
  const res = await fetch(endpoint(path, env), {
    method,
    headers: {
      Authorization: `Bearer ${env.GSD_PLAN_CAPABILITY_TOKEN}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  const json = await res.json().catch(() => ({}));
  return {
    content: [{ type: "text", text: JSON.stringify(json) }],
    isError: !res.ok || Boolean(json.error),
    details: json,
  };
}

function encodePath(value) {
  return encodeURIComponent(value);
}

export function registerPlanTools(pi, env = process.env) {
  if (!hasPlanCapability(env)) return false;

  pi.registerTool({
    name: "plan_get_project_state",
    label: "Get project plan state",
    description:
      "Read the active project plan, paused summaries, archive summaries, Plan Mode state, and supported actions.",
    parameters: Type.Object({}),
    execute: (_id, _params, signal) => requestPlan("/project-state", { signal }, env),
  });

  pi.registerTool({
    name: "plan_get_archived_plan",
    label: "Get archived project plan",
    description: "Read the full detail for an archived project plan.",
    parameters: Type.Object({
      planId: Type.String(),
    }),
    execute: (_id, params, signal) =>
      requestPlan(`/archived/${encodePath(params.planId)}`, { signal }, env),
  });

  pi.registerTool({
    name: "plan_create",
    label: "Create project plan",
    description: "Create a new active project plan with ordered items.",
    parameters: Type.Object({
      title: Type.String(),
      items: Type.Array(
        Type.Object({
          title: Type.String(),
          description: Type.Optional(Type.String()),
        }),
      ),
      mutationId: Type.String(),
      toolCallId: Type.String(),
    }),
    execute: (_id, params, signal) =>
      requestPlan("/plans", { method: "POST", body: params, signal }, env),
  });

  pi.registerTool({
    name: "plan_rename",
    label: "Rename project plan",
    description: "Rename an existing project plan.",
    parameters: Type.Object({
      planId: Type.String(),
      title: Type.String(),
      ...MutationMetaParams,
    }),
    execute: (_id, params, signal) =>
      requestPlan(
        `/plans/${encodePath(params.planId)}/rename`,
        { method: "PATCH", body: params, signal },
        env,
      ),
  });

  pi.registerTool({
    name: "plan_pause",
    label: "Pause project plan",
    description: "Pause an active project plan.",
    parameters: Type.Object({
      planId: Type.String(),
      note: Type.Optional(Type.String()),
      ...MutationMetaParams,
    }),
    execute: (_id, params, signal) =>
      requestPlan(
        `/plans/${encodePath(params.planId)}/pause`,
        { method: "POST", body: params, signal },
        env,
      ),
  });

  pi.registerTool({
    name: "plan_resume",
    label: "Resume project plan",
    description: "Resume a paused project plan.",
    parameters: Type.Object({
      planId: Type.String(),
      ...MutationMetaParams,
    }),
    execute: (_id, params, signal) =>
      requestPlan(
        `/plans/${encodePath(params.planId)}/resume`,
        { method: "POST", body: params, signal },
        env,
      ),
  });

  pi.registerTool({
    name: "plan_archive",
    label: "Archive project plan",
    description: "Archive a project plan.",
    parameters: Type.Object({
      planId: Type.String(),
      ...MutationMetaParams,
    }),
    execute: (_id, params, signal) =>
      requestPlan(
        `/plans/${encodePath(params.planId)}/archive`,
        { method: "POST", body: params, signal },
        env,
      ),
  });

  pi.registerTool({
    name: "plan_add_item",
    label: "Add project plan item",
    description: "Add a new item to a project plan.",
    parameters: Type.Object({
      planId: Type.String(),
      title: Type.String(),
      description: Type.Optional(Type.String()),
      afterItemId: Type.Optional(Type.String()),
      ...MutationMetaParams,
    }),
    execute: (_id, params, signal) =>
      requestPlan(
        `/plans/${encodePath(params.planId)}/items`,
        { method: "POST", body: params, signal },
        env,
      ),
  });

  pi.registerTool({
    name: "plan_update_item",
    label: "Update project plan item",
    description: "Update a project plan item title, description, status, notes, or result.",
    parameters: Type.Object({
      planId: Type.String(),
      itemId: Type.String(),
      title: Type.Optional(Type.String()),
      description: Type.Optional(Type.String()),
      status: Type.Optional(PlanItemStatus),
      agentNotes: Type.Optional(Type.String()),
      result: Type.Optional(Type.String()),
      ...MutationMetaParams,
    }),
    execute: (_id, params, signal) =>
      requestPlan(
        `/plans/${encodePath(params.planId)}/items/${encodePath(params.itemId)}`,
        { method: "PATCH", body: params, signal },
        env,
      ),
  });

  pi.registerTool({
    name: "plan_cancel_item",
    label: "Cancel project plan item",
    description: "Cancel a project plan item.",
    parameters: Type.Object({
      planId: Type.String(),
      itemId: Type.String(),
      agentNotes: Type.Optional(Type.String()),
      ...MutationMetaParams,
    }),
    execute: (_id, params, signal) =>
      requestPlan(
        `/plans/${encodePath(params.planId)}/items/${encodePath(params.itemId)}/cancel`,
        { method: "POST", body: params, signal },
        env,
      ),
  });

  pi.registerTool({
    name: "plan_reorder_items",
    label: "Reorder project plan items",
    description: "Replace the item order for a project plan.",
    parameters: Type.Object({
      planId: Type.String(),
      orderedItemIds: Type.Array(Type.String()),
      ...MutationMetaParams,
    }),
    execute: (_id, params, signal) =>
      requestPlan(
        `/plans/${encodePath(params.planId)}/items/reorder`,
        { method: "POST", body: params, signal },
        env,
      ),
  });

  pi.registerTool({
    name: "plan_update_user_context",
    label: "Update project plan user context",
    description: "Update human-provided context for a plan or item.",
    parameters: Type.Object({
      planId: Type.String(),
      target: Type.Union([
        Type.Object({ type: Type.Literal("plan") }),
        Type.Object({ type: Type.Literal("item"), itemId: Type.String() }),
      ]),
      userContext: Type.String(),
      ...MutationMetaParams,
    }),
    execute: (_id, params, signal) =>
      requestPlan("/user-context", { method: "PATCH", body: params, signal }, env),
  });

  return true;
}
