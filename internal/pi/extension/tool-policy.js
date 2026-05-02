export function toolProfile(env = process.env) {
  const value = String(env.GSD_TOOL_PROFILE ?? "").trim().toLowerCase();
  return value || "full";
}

export function isMinimalToolProfile(env = process.env) {
  return toolProfile(env) === "minimal";
}
