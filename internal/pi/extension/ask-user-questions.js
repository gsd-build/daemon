import { Type } from "@sinclair/typebox";

const OptionSchema = Type.Object({
  label: Type.String({ minLength: 1 }),
  description: Type.Optional(Type.String()),
  preview: Type.Optional(Type.String()),
});

const QuestionSchema = Type.Object({
  id: Type.String({ minLength: 1 }),
  header: Type.Optional(Type.String()),
  question: Type.String({ minLength: 1 }),
  options: Type.Array(OptionSchema, { minItems: 1 }),
  allowMultiple: Type.Optional(Type.Boolean()),
});

const ParamsSchema = Type.Object({
  questions: Type.Array(QuestionSchema, { minItems: 1 }),
});

const recentSignatures = new Map();
const DEDUPE_WINDOW_MS = 1500;
export const STRUCTURED_QUESTION_PLACEHOLDER_PREFIX = "__gsd_structured_questions__:";

export const askUserQuestionsTool = {
  name: "ask_user_questions",
  label: "Ask structured questions",
  description:
    "Ask the human one or more structured questions using browser-native choice cards. Use concise labels and put long explanation in normal assistant prose before calling this tool.",
  parameters: ParamsSchema,
  async execute(_toolCallId, params, signal, _onUpdate, ctx) {
    const questions = params.questions.map(normalizeQuestion);
    const signature = JSON.stringify(questions);
    const now = Date.now();
    pruneRecentSignatures(now);

    if (recentSignatures.has(signature) && now - recentSignatures.get(signature) < DEDUPE_WINDOW_MS) {
      return {
        content: [{ type: "text", text: "Duplicate structured question request ignored." }],
        isError: false,
        details: {},
      };
    }
    recentSignatures.set(signature, now);

    const response = await ctx.ui.input(
      `Structured question round ready (${questions.length} question${questions.length === 1 ? "" : "s"})`,
      encodeStructuredQuestionPlaceholder(questions),
      { signal },
    );

    if (response === undefined) {
      return {
        content: [{ type: "text", text: "(human cancelled or did not answer)" }],
        isError: true,
        details: {},
      };
    }

    return {
      content: [{ type: "text", text: formatForLLM(response) }],
      isError: false,
      details: {},
    };
  },
};

function normalizeQuestion(question) {
  return {
    id: question.id,
    header: question.header || question.question,
    question: question.question,
    allowMultiple: Boolean(question.allowMultiple),
    options: question.options.map((option) => ({
      label: option.label,
      description: option.description || "",
      preview: option.preview || "",
    })),
  };
}

function encodeStructuredQuestionPlaceholder(questions) {
  return `${STRUCTURED_QUESTION_PLACEHOLDER_PREFIX}${Buffer.from(JSON.stringify({ questions }), "utf8").toString("base64")}`;
}

function pruneRecentSignatures(now) {
  const cutoff = now - DEDUPE_WINDOW_MS;
  for (const [signature, timestamp] of recentSignatures) {
    if (timestamp < cutoff) {
      recentSignatures.delete(signature);
    }
  }
}

function formatForLLM(response) {
  try {
    return JSON.stringify(JSON.parse(response), null, 2);
  } catch {
    return response;
  }
}
