import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { buildClaudeCliBrowserTools, mergeClaudeCliTools } from "./index.ts";

const browserGrant = {
  grantId: "grant_1",
  browserId: "browser_1",
  sessionId: "session_1",
};

describe("browser tool registration", () => {
  it("surfaces gsd_browser when context supplies browser grant", () => {
    const tools = buildClaudeCliBrowserTools({ browserGrant });
    assert.equal(tools.some((tool) => tool.name === "gsd_browser"), true);
  });

  it("does not surface gsd_browser without a browser grant", () => {
    const tools = buildClaudeCliBrowserTools({});
    assert.equal(tools.some((tool) => tool.name === "gsd_browser"), false);
  });

  it("describes only supported bare browser methods", () => {
    const [tool] = buildClaudeCliBrowserTools({ browserGrant });
    assert.ok(tool);
    assert.deepEqual(tool.input_schema.properties.method.enum.includes("navigate"), true);
    assert.deepEqual(tool.input_schema.properties.method.enum.includes("browser.navigate"), false);
    assert.match(tool.description, /do not prefix/i);
  });

  it("adds gsd_browser when pi context does not include it", () => {
    const tools = mergeClaudeCliTools([{ name: "ask_human", description: "Ask", parameters: {} }], browserGrant);
    assert.equal(tools.filter((tool) => tool.name === "gsd_browser").length, 1);
    assert.equal(tools.some((tool) => tool.name === "ask_human"), true);
  });

  it("keeps the pi-registered gsd_browser when context already includes it", () => {
    const registeredBrowserTool = {
      name: "gsd_browser",
      description: "Registered browser tool",
      parameters: {},
    };
    const tools = mergeClaudeCliTools([
      registeredBrowserTool,
      { name: "ask_human", description: "Ask", parameters: {} },
    ], browserGrant);

    assert.equal(tools.filter((tool) => tool.name === "gsd_browser").length, 1);
    assert.equal(tools.find((tool) => tool.name === "gsd_browser"), registeredBrowserTool);
    assert.equal(tools.some((tool) => tool.name === "ask_human"), true);
  });

  it("filters a pi-registered gsd_browser when no browser grant exists", () => {
    const tools = mergeClaudeCliTools([
      { name: "gsd_browser", description: "Registered browser tool", parameters: {} },
      { name: "ask_human", description: "Ask", parameters: {} },
    ], undefined);

    assert.equal(tools.some((tool) => tool.name === "gsd_browser"), false);
    assert.equal(tools.some((tool) => tool.name === "ask_human"), true);
  });
});
