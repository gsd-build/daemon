import { Type } from "@sinclair/typebox";

const ExecutionSubTask = Type.Object({
  id: Type.Optional(Type.String()),
  text: Type.String(),
});

const ExecutionCriterion = Type.Object({
  id: Type.Optional(Type.String()),
  text: Type.String(),
});

const PlanIntentMetaParams = {
  idempotencyKey: Type.Optional(Type.String()),
  toolCallId: Type.Optional(Type.String()),
};

const PlanNextParams = Type.Object({
  ...PlanIntentMetaParams,
  planId: Type.Optional(Type.String({ format: "uuid" })),
  selector: Type.Optional(
    Type.Object({
      itemId: Type.Optional(Type.String({ format: "uuid" })),
      titleIncludes: Type.Optional(Type.String()),
    }),
  ),
  createPlan: Type.Optional(
    Type.Object({
      title: Type.String(),
      items: Type.Array(
        Type.Object({
          title: Type.String(),
          description: Type.Optional(Type.String()),
          dependsOn: Type.Optional(Type.Array(Type.String({ format: "uuid" }))),
        }),
        { minItems: 1, maxItems: 100 },
      ),
    }),
  ),
  leaseTtlSeconds: Type.Optional(Type.Integer({ minimum: 1, maximum: 900 })),
});

const PlanDoneParams = Type.Object({
  ...PlanIntentMetaParams,
  planId: Type.Optional(Type.String({ format: "uuid" })),
  itemId: Type.Optional(Type.String({ format: "uuid" })),
  summary: Type.String(),
  blockers: Type.Optional(Type.Array(Type.String(), { maxItems: 20 })),
  criteriaMet: Type.Optional(Type.Array(Type.String(), { maxItems: 10 })),
  evidenceRefs: Type.Optional(Type.Array(Type.String({ format: "uuid" }), { maxItems: 20 })),
  evidencePolicy: Type.Optional(
    Type.Union([Type.Literal("auto"), Type.Literal("explicit"), Type.Literal("waive")]),
  ),
  evidenceWaiverReason: Type.Optional(Type.String()),
  advance: Type.Optional(Type.Boolean()),
});

export function hasPlanCapability(env = process.env) {
  return Boolean(
    env.GSD_PLAN_API_BASE_URL &&
      env.GSD_PLAN_CAPABILITY_ATTEMPT_ID &&
      env.GSD_PLAN_CAPABILITY_TOKEN &&
      env.GSD_PLAN_CAPABILITY_EXPIRES_AT,
  );
}

function endpoint(path, env = process.env) {
  return `${env.GSD_PLAN_API_BASE_URL.replace(/\/$/, "")}/api/agent-plan${path}`;
}

async function fetchPlanJson(path, { method = "GET", body, signal } = {}, env = process.env) {
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
  return { res, json };
}

async function requestPlan(path, { method = "GET", body, signal } = {}, env = process.env) {
  const { res, json } = await fetchPlanJson(path, { method, body, signal }, env);
  return {
    content: [{ type: "text", text: JSON.stringify(json) }],
    isError: !res.ok || Boolean(json.error) || json?.ok === false,
    details: json,
  };
}

function encodePath(value) {
  return encodeURIComponent(value);
}

function planIntentBody(toolCallId, params) {
  return {
    ...params,
    toolCallId: params?.toolCallId ?? toolCallId,
  };
}

function requestPlanIntent(tool, toolCallId, params, signal, env) {
  return requestPlan(
    `/intent/${encodePath(tool)}`,
    {
      method: "POST",
      body: planIntentBody(toolCallId, params),
      signal,
    },
    env,
  );
}

export function registerPlanTools(pi, env = process.env) {
  if (!hasPlanCapability(env)) return false;

  const intentTools = [
    {
      name: "plan_status",
      label: "Read project plan status",
      description:
        "Read compact planning state, including active item, progress, next item, blockers, and allowed next intent.",
      parameters: Type.Object({
        ...PlanIntentMetaParams,
        detail: Type.Optional(Type.Union([Type.Literal("compact"), Type.Literal("full")])),
      }),
    },
    {
      name: "plan_next",
      label: "Start next plan item",
      description:
        "Choose or create the next runnable plan item. The runtime owns leases, dependencies, idempotency, and next-item selection.",
      parameters: PlanNextParams,
    },
    {
      name: "plan_checkpoint",
      label: "Record plan progress",
      description:
        "Record progress, execution contract, notes, criteria, or blocker state for the current item.",
      parameters: Type.Object({
        ...PlanIntentMetaParams,
        planId: Type.Optional(Type.String({ format: "uuid" })),
        itemId: Type.Optional(Type.String({ format: "uuid" })),
        summary: Type.Optional(Type.String()),
        subTasks: Type.Optional(Type.Array(ExecutionSubTask, { maxItems: 20 })),
        agentCriteria: Type.Optional(Type.Array(ExecutionCriterion, { maxItems: 10 })),
        criteriaMet: Type.Optional(Type.Array(Type.String(), { maxItems: 10 })),
        blocked: Type.Optional(
          Type.Object({
            reason: Type.String(),
            nextAction: Type.Optional(Type.String()),
          }),
        ),
      }),
    },
    {
      name: "plan_sync",
      label: "Sync project plan state",
      description: "Refresh compact planning state after long work, errors, or before final response.",
      parameters: Type.Object({
        ...PlanIntentMetaParams,
        detail: Type.Optional(Type.Union([Type.Literal("compact"), Type.Literal("full")])),
        cursor: Type.Optional(Type.String()),
      }),
    },
    {
      name: "plan_done",
      label: "Complete current plan item",
      description:
        "Complete the current item using runtime evidence and optionally start the next runnable item.",
      parameters: PlanDoneParams,
    },
  ];

  for (const tool of intentTools) {
    pi.registerTool({
      ...tool,
      execute: (toolCallId, params, signal) =>
        requestPlanIntent(tool.name, toolCallId, params, signal, env),
    });
  }

  return true;
}
