import assert from "node:assert/strict";
import { STRUCTURED_QUESTION_PLACEHOLDER_PREFIX, askUserQuestionsTool } from "./ask-user-questions.js";
import { schemaToZod } from "./schema-to-zod.js";

const schema = schemaToZod(askUserQuestionsTool.parameters);
schema.parse({
  questions: [
    {
      id: "scope",
      header: "Choose scope",
      question: "What should happen first?",
      options: [{ label: "Daemon bridge", preview: '{"path":"internal/session/actor.go"}' }],
      allowMultiple: false,
    },
  ],
});

const result = await askUserQuestionsTool.execute(
  "toolu_123",
  {
    questions: [
      {
        id: "scope",
        question: "What should happen first?",
        options: [{ label: "Daemon bridge" }],
      },
    ],
  },
  undefined,
  undefined,
  {
    ui: {
      async input(title, placeholder) {
        assert.equal(title, "Structured question round ready (1 question)");
        assert.equal(placeholder.startsWith(STRUCTURED_QUESTION_PLACEHOLDER_PREFIX), true);
        return JSON.stringify({ answers: { scope: { answers: ["Daemon bridge"] } } });
      },
    },
  },
);

assert.equal(result.isError, false);
assert.match(result.content[0].text, /Daemon bridge/);
console.log("ask-user-questions tests passed");
