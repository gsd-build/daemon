import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { buildClaudeCliBrowserTools, mergeClaudeCliTools } from "./index.ts";
import {
  BROWSER_METHOD_CATEGORY,
  BROWSER_TOOL_CATEGORIES,
  BROWSER_TOOL_METHODS,
} from "./browser-methods.ts";

const browserGrant = {
  grantId: "grant_1",
  sessionId: "session_1",
  taskId: "task_1",
  channelId: "channel_1",
  projectId: "project_1",
  machineId: "machine_1",
  expiresAt: "2026-05-01T12:00:00Z",
};

describe("browser tool registration", () => {
  it("surfaces gsd_browser when context supplies browser grant", () => {
    const tools = buildClaudeCliBrowserTools({ browserGrant });
    assert.equal(tools.some((tool) => tool.name === "gsd_browser"), true);
  });

  it("surfaces gsd_browser even without a browser grant", () => {
    const tools = buildClaudeCliBrowserTools({});
    assert.equal(tools.some((tool) => tool.name === "gsd_browser"), true);
  });

  it("describes the bundled skill routing behavior", () => {
    const [tool] = buildClaudeCliBrowserTools({});
    assert.ok(tool);
    assert.deepEqual(tool.input_schema.properties.method.enum.includes("navigate"), true);
    assert.deepEqual(tool.input_schema.properties.method.enum.includes("visual_diff"), true);
    assert.deepEqual(tool.input_schema.properties.method.enum.includes("vault_login"), false);
    assert.deepEqual(tool.input_schema.properties.method.enum.includes("browser.navigate"), false);
    assert.deepEqual(tool.input_schema.properties.category.enum, BROWSER_TOOL_CATEGORIES);
    assert.match(tool.description, /rendered UI/i);
    assert.match(tool.promptSnippet, /Load the gsd-browser skill/i);
  });

  it("keeps browser method registry and categories explicit", () => {
    assert.deepEqual(BROWSER_TOOL_METHODS, [
      "navigate",
      "back",
      "forward",
      "reload",
      "list_pages",
      "switch_page",
      "close_page",
      "list_frames",
      "select_frame",
      "click",
      "type",
      "press",
      "hover",
      "scroll",
      "select_option",
      "set_checked",
      "drag",
      "set_viewport",
      "click_ref",
      "hover_ref",
      "fill_ref",
      "emulate_device",
      "upload_file",
      "debug_bundle",
      "screenshot",
      "zoom_region",
      "save_pdf",
      "visual_diff",
      "generate_test",
      "har_export",
      "trace_start",
      "trace_stop",
      "snapshot",
      "get_ref",
      "accessibility_tree",
      "find",
      "page_source",
      "assert",
      "diff",
      "wait_for",
      "analyze_form",
      "find_best",
      "console",
      "network",
      "dialog",
      "timeline",
      "session_summary",
      "extract",
      "action_cache",
      "check_injection",
      "eval",
      "fill_form",
      "act",
      "mock_route",
      "block_urls",
      "clear_routes",
      "batch",
    ]);
    assert.equal(BROWSER_METHOD_CATEGORY.eval, "external_effect");
    assert.equal(BROWSER_METHOD_CATEGORY.batch, "composite");
    assert.equal(BROWSER_METHOD_CATEGORY.vault_login, "credential_auth");
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

  it("adds the ambient gsd_browser when no browser grant exists", () => {
    const tools = mergeClaudeCliTools([
      { name: "gsd_browser", description: "Registered browser tool", parameters: {} },
      { name: "ask_human", description: "Ask", parameters: {} },
    ], undefined);

    assert.equal(tools.some((tool) => tool.name === "gsd_browser"), true);
    assert.equal(tools.some((tool) => tool.name === "ask_human"), true);
  });
});
