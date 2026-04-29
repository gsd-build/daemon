import assert from "node:assert/strict";
import test from "node:test";
import { createWarmClaudeSdkWorkerForTest } from "./claude-sdk-worker.ts";

test("warm Claude SDK worker keeps one query pump across two turns", async () => {
  const worker = createWarmClaudeSdkWorkerForTest({
    async *query({ prompt }) {
      let count = 0;
      for await (const _msg of prompt) {
        count += 1;
        yield {
          type: "stream_event",
          event: { type: "content_block_start", content_block: { type: "text" } },
        };
        yield {
          type: "stream_event",
          event: { type: "content_block_delta", delta: { type: "text_delta", text: `turn-${count}` } },
        };
        yield {
          type: "stream_event",
          event: { type: "content_block_stop" },
        };
        yield {
          type: "result",
          subtype: "success",
          duration_ms: 1,
          duration_api_ms: 1,
          is_error: false,
          num_turns: count,
          session_id: "fake-session",
          total_cost_usd: 0,
          usage: { input_tokens: count, output_tokens: count },
        };
      }
    },
  });

  const seen = [];
  const first = await worker.turn({
    messages: [{
      type: "user",
      message: { role: "user", content: "first" },
      parent_tool_use_id: null,
      session_id: "fake-session",
    }],
    onMessage(message) {
      if (message.type === "stream_event" && message.event?.type === "content_block_delta") {
        seen.push(message.event.delta.text);
      }
    },
  });
  const second = await worker.turn({
    messages: [{
      type: "user",
      message: { role: "user", content: "second" },
      parent_tool_use_id: null,
      session_id: "fake-session",
    }],
    onMessage(message) {
      if (message.type === "stream_event" && message.event?.type === "content_block_delta") {
        seen.push(message.event.delta.text);
      }
    },
  });
  await worker.stop();

  assert.equal(first.result?.type, "result");
  assert.equal(second.result?.type, "result");
  assert.deepEqual(seen, ["turn-1", "turn-2"]);
  assert.equal(worker.queryStartsForTest(), 1);
});
