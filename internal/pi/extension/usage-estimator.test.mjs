import assert from "node:assert/strict";
import {
  applyUsageFromSdkMessage,
  ensureNonZeroUsageForAbortedToolTurn,
  extractUsage,
} from "./usage-estimator.js";

const cost = { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 };

assert.deepEqual(extractUsage({
  type: "stream_event",
  event: {
    type: "message_delta",
    usage: {
      input_tokens: 12,
      output_tokens: 5,
      cache_read_input_tokens: 2,
      cache_creation_input_tokens: 1,
    },
  },
}), { input: 12, output: 5, cacheRead: 2, cacheWrite: 1 });

const usage = {
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  totalTokens: 0,
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
};
assert.equal(applyUsageFromSdkMessage(usage, { usage: { input: 7, output: 3 } }, cost), true);
assert.equal(usage.input, 7);
assert.equal(usage.output, 3);
assert.equal(usage.totalTokens, 10);
assert.equal(usage.cost.output, 0.000045);

const estimated = {
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  totalTokens: 0,
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
};
assert.equal(ensureNonZeroUsageForAbortedToolTurn(estimated, [
  { type: "toolCall", name: "bash", arguments: { command: "pwd" } },
], cost), true);
assert.ok(estimated.output > 0);
assert.ok(estimated.cost.total > 0);
