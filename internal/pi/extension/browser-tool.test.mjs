import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { buildClaudeCliToolsForTest } from "./index.ts";

describe("browser tool registration", () => {
  it("surfaces gsd_browser when context supplies browser grant", () => {
    const tools = buildClaudeCliToolsForTest({
      browserGrant: {
        grantId: "grant_1",
        browserId: "browser_1",
        sessionId: "session_1",
      },
    });
    assert.equal(tools.some((tool) => tool.name === "gsd_browser"), true);
  });
});
