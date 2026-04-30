import { readdir, readFile } from "node:fs/promises";
import path from "node:path";

export async function listAgents(agentDir = process.env.GSD_AGENT_DIR) {
  if (!agentDir) return [];
  let entries;
  try {
    entries = await readdir(agentDir, { withFileTypes: true });
  } catch {
    return [];
  }
  const agents = [];
  for (const entry of entries) {
    if (!entry.isFile() || !entry.name.endsWith(".json")) continue;
    try {
      const parsed = JSON.parse(await readFile(path.join(agentDir, entry.name), "utf8"));
      if (parsed && typeof parsed.name === "string") {
        agents.push(parsed);
      }
    } catch {
      continue;
    }
  }
  return agents.sort((a, b) => a.name.localeCompare(b.name));
}

export async function findAgent(name, agentDir = process.env.GSD_AGENT_DIR) {
  const agents = await listAgents(agentDir);
  return agents.find((agent) => agent.name === name) ?? null;
}
