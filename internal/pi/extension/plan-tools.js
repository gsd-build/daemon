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

function pickDefined(fields) {
  return Object.fromEntries(Object.entries(fields).filter(([, value]) => value !== undefined));
}

function arrayOrEmpty(value) {
  return Array.isArray(value) ? value : [];
}

function objectArrayOrEmpty(value) {
  return arrayOrEmpty(value).filter(
    (item) => item !== null && typeof item === "object" && !Array.isArray(item),
  );
}

function objectOrNull(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value : null;
}

function truncateText(value, maxLength = 4000) {
  if (typeof value !== "string") return value;
  if (value.length <= maxLength) return value;
  return `${value.slice(0, maxLength)}...[truncated ${value.length - maxLength} chars]`;
}

function compactPlanItemSummary(item) {
  return pickDefined({
    id: item.id,
    title: item.title,
    status: item.status,
    position: item.position,
    startedAt: item.startedAt,
    blockedAt: item.blockedAt,
    completedAt: item.completedAt,
    cancelledAt: item.cancelledAt,
    updatedAt: item.updatedAt,
  });
}

function compactPlanItemDetail(item) {
  if (!item) return null;
  return pickDefined({
    ...compactPlanItemSummary(item),
    description: truncateText(item.description, 6000),
    userContext: truncateText(item.userContext, 4000),
    agentNotes: truncateText(item.agentNotes, 4000),
    result: truncateText(item.result, 4000),
  });
}

function compactPlanSummary(plan) {
  if (!plan) return null;
  return pickDefined({
    id: plan.id,
    projectId: plan.projectId,
    title: plan.title,
    status: plan.status,
    revision: plan.revision,
    progress: plan.progress,
    nextItemTitle: plan.nextItemTitle,
    currentItemId: plan.currentItemId,
    updatedAt: plan.updatedAt,
    archivedAt: plan.archivedAt,
  });
}

export function compactProjectState(json) {
  const snapshot = objectOrNull(json?.snapshot);
  if (!snapshot) return json;

  const activePlan = objectOrNull(snapshot.activePlan);
  const activeItems = objectArrayOrEmpty(activePlan?.items);
  const sortedItems = [...activeItems].sort((a, b) => (a.position ?? 0) - (b.position ?? 0));
  const currentItem =
    sortedItems.find((item) => item.id === activePlan?.currentItemId) ??
    sortedItems.find((item) => item.status === "in_progress") ??
    null;
  const nextItem = sortedItems.find((item) => item.status === "pending") ?? null;
  const blockedItems = sortedItems.filter((item) => item.status === "blocked").map(compactPlanItemDetail);

  return {
    snapshot: pickDefined({
      projectId: snapshot.projectId,
      planModeEnabled: snapshot.planModeEnabled,
      revision: snapshot.revision,
      activePlan: activePlan
        ? pickDefined({
            ...compactPlanSummary(activePlan),
            userContext: truncateText(activePlan.userContext, 4000),
            agentNotes: truncateText(activePlan.agentNotes, 4000),
            currentItem: compactPlanItemDetail(currentItem),
            nextItem: compactPlanItemDetail(nextItem),
            blockedItems,
            items: sortedItems.map(compactPlanItemSummary),
          })
        : null,
      pausedPlans: objectArrayOrEmpty(snapshot.pausedPlans).map(compactPlanSummary),
      archivedPlans: objectArrayOrEmpty(snapshot.archivedPlans).map(compactPlanSummary),
      supportedActions: snapshot.supportedActions,
      updatedAt: snapshot.updatedAt,
      updatedBySessionId: snapshot.updatedBySessionId,
      updatedByTaskId: snapshot.updatedByTaskId,
    }),
    detail: "compact",
    fullDetail: 'Call plan_get_project_state with {"detail":"full"} for complete item descriptions and plan bodies.',
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
      "Read compact project plan state by default. Request detail full only when complete item descriptions and plan bodies are needed.",
    parameters: Type.Object({
      detail: Type.Optional(Type.Union([Type.Literal("compact"), Type.Literal("full")])),
    }),
    execute: async (_id, params, signal) => {
      const result = await requestPlan("/project-state", { signal }, env);
      if (params?.detail === "full" || result.isError) return result;
      const compact = compactProjectState(result.details);
      return {
        ...result,
        content: [{ type: "text", text: JSON.stringify(compact) }],
        details: compact,
      };
    },
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
