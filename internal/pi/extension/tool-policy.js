export function parseAllowedTools(value) {
  if (!value) return new Set();
  return new Set(String(value).split(",").map((item) => item.trim()).filter(Boolean));
}

export function isToolAllowed(category, allowed) {
  return allowed.has(category);
}

export function registerIfAllowed(pi, allowed, category, definition) {
  if (!isToolAllowed(category, allowed)) return false;
  pi.registerTool(definition);
  return true;
}

export function hasSubagentToolPolicy(env = process.env) {
  return Object.prototype.hasOwnProperty.call(env, "GSD_SUBAGENT_ALLOWED_TOOLS");
}

export function toolProfile(env = process.env) {
  const value = String(env.GSD_TOOL_PROFILE ?? "").trim().toLowerCase();
  return value || "full";
}

export function isMinimalToolProfile(env = process.env) {
  return toolProfile(env) === "minimal";
}

export function categoryForToolName(name) {
  if (!name) return null;
  if (name === "gsd_browser") return "browser";
  if (name.startsWith("background_") || name === "shell_exec") return "shell";
  if (/^(read|view|cat|file_read|read_file)(_|$)/.test(name) || name === "read") return "read";
  if (/^(search|grep|glob|find|list)(_|$)/.test(name) || name === "search") return "search";
  if (/^(write|edit|patch|create|delete|move|rename)(_|$)/.test(name) || name === "write") return "write";
  if (/^(test|verify|lint|typecheck|build)(_|$)/.test(name) || name === "test") return "test";
  if (/^(bash|shell|terminal|exec)(_|$)/.test(name)) return "shell";
  return null;
}

export function filterToolsByPolicy(tools, allowed, env = process.env) {
  if (!hasSubagentToolPolicy(env)) return tools;
  return tools.filter((tool) => {
    const category = categoryForToolName(tool?.name);
    if (!category) return false;
    return isToolAllowed(category, allowed);
  });
}
