import assert from "node:assert/strict";
import { z } from "zod";
import { schemaToZod } from "./schema-to-zod.js";

const schema = {
  type: "object",
  properties: {
    questions: {
      type: "array",
      minItems: 1,
      items: {
        type: "object",
        properties: {
          id: { type: "string", minLength: 1 },
          header: { type: "string" },
          question: { type: "string", minLength: 1 },
          allowMultiple: { type: "boolean" },
          options: {
            type: "array",
            items: {
              type: "object",
              properties: {
                label: { type: "string", minLength: 1 },
                description: { type: "string" },
                preview: { type: "string" },
              },
              required: ["label"],
            },
          },
        },
        required: ["id", "question", "options"],
      },
    },
  },
  required: ["questions"],
};

const parsed = schemaToZod(schema).parse({
  questions: [
    {
      id: "scope",
      question: "What should the agent build?",
      options: [{ label: "Daemon bridge", description: "Wire structured questions through Pi." }],
    },
  ],
});

assert.equal(parsed.questions[0].options[0].label, "Daemon bridge");
assert.throws(() => schemaToZod(schema).parse({ questions: [] }), z.ZodError);
assert.throws(() => schemaToZod(schema).parse({ questions: [{ id: "", question: "", options: [] }] }), z.ZodError);
assert.equal(schemaToZod({ enum: [1, "two", false, null] }).parse(1), 1);
assert.equal(schemaToZod({ enum: [1, "two", false, null] }).parse(false), false);
assert.throws(() => schemaToZod({ enum: [1, "two", false, null] }).parse("1"), z.ZodError);
assert.equal(schemaToZod({ const: "task_done" }).parse("task_done"), "task_done");
assert.throws(() => schemaToZod({ const: "task_done" }).parse("task_next"), z.ZodError);
assert.throws(() => schemaToZod({ type: "integer", minimum: 1, maximum: 900 }).parse(1800), z.ZodError);
assert.throws(() => schemaToZod({ type: "string", format: "uuid" }).parse("main.py:23"), z.ZodError);

const operationSchema = schemaToZod({
  anyOf: [
    {
      type: "object",
      properties: {
        type: { const: "start_next_item" },
        leaseTtlSeconds: { type: "integer", minimum: 1, maximum: 900 },
      },
      required: ["type"],
    },
    {
      type: "object",
      properties: {
        type: { const: "create_task" },
        title: { type: "string" },
        items: { type: "array", items: { type: "object" } },
      },
      required: ["type", "title", "items"],
    },
  ],
});
const operationResult = operationSchema.safeParse({
  type: "start_next_item",
  leaseTtlSeconds: 1800,
});
assert.equal(operationResult.success, false);
assert.deepEqual(
  operationResult.error.issues.map((issue) => issue.path.join(".")),
  ["leaseTtlSeconds"],
);
assert.equal(operationSchema.safeParse({
  type: "create_task",
  title: "Wire task flow",
  items: [{}],
}).success, true);
assert.equal(operationSchema.safeParse({
  type: "create_plan",
  title: "Legacy value",
  items: [{}],
}).success, false);
console.log("schema-to-zod tests passed");
