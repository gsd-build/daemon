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
console.log("schema-to-zod tests passed");
