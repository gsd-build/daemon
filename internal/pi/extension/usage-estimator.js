export function applyUsageFromSdkMessage(usage, message, cost) {
  const extracted = extractUsage(message);
  if (!extracted) return false;
  mergeUsage(usage, extracted);
  computeCost(usage, cost);
  return true;
}

export function ensureNonZeroUsageForAbortedToolTurn(usage, content, cost) {
  if (usage.output > 0) {
    computeCost(usage, cost);
    return false;
  }

  // Aborted tool-surfacing turns use local token estimates when the SDK stream
  // ends before a usage-bearing result frame is available.
  usage.output = estimateOutputTokens(content);
  usage.totalTokens = usage.input + usage.output + usage.cacheRead + usage.cacheWrite;
  computeCost(usage, cost);
  return usage.output > 0;
}

export function extractUsage(message) {
  const candidates = [
    message?.usage,
    message?.message?.usage,
    message?.delta?.usage,
    message?.event?.usage,
    message?.event?.message?.usage,
    message?.event?.delta?.usage,
  ];

  for (const candidate of candidates) {
    const normalized = normalizeUsage(candidate);
    if (normalized) return normalized;
  }
  return null;
}

export function estimateOutputTokens(content) {
  let chars = 0;
  for (const block of content ?? []) {
    if (block?.type === "text") {
      chars += String(block.text ?? "").length;
    } else if (block?.type === "toolCall") {
      chars += String(block.name ?? "").length + JSON.stringify(block.arguments ?? {}).length;
    }
  }
  return Math.max(1, Math.ceil(chars / 4));
}

function normalizeUsage(usage) {
  if (!usage || typeof usage !== "object") return null;

  const normalized = {
    input: numberFrom(usage.input_tokens, usage.input),
    output: numberFrom(usage.output_tokens, usage.output),
    cacheRead: numberFrom(usage.cache_read_input_tokens, usage.cacheRead),
    cacheWrite: numberFrom(usage.cache_creation_input_tokens, usage.cacheWrite),
  };

  if (normalized.input === 0 && normalized.output === 0 && normalized.cacheRead === 0 && normalized.cacheWrite === 0) {
    return null;
  }
  return normalized;
}

function numberFrom(...values) {
  for (const value of values) {
    if (typeof value === "number" && Number.isFinite(value)) {
      return Math.max(0, Math.floor(value));
    }
  }
  return 0;
}

function mergeUsage(target, source) {
  target.input = Math.max(target.input, source.input);
  target.output = Math.max(target.output, source.output);
  target.cacheRead = Math.max(target.cacheRead, source.cacheRead);
  target.cacheWrite = Math.max(target.cacheWrite, source.cacheWrite);
  target.totalTokens = target.input + target.output + target.cacheRead + target.cacheWrite;
}

function computeCost(usage, cost) {
  if (!cost) return;
  usage.cost.input = (cost.input / 1_000_000) * usage.input;
  usage.cost.output = (cost.output / 1_000_000) * usage.output;
  usage.cost.cacheRead = (cost.cacheRead / 1_000_000) * usage.cacheRead;
  usage.cost.cacheWrite = (cost.cacheWrite / 1_000_000) * usage.cacheWrite;
  usage.cost.total = usage.cost.input + usage.cost.output + usage.cost.cacheRead + usage.cost.cacheWrite;
}
