import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  BROWSER_METHOD_CATEGORY,
  BROWSER_TOOL_METHODS,
} from "./browser-methods.generated.ts";

describe("browser methods registry", () => {
  it("keeps credential and vault methods policy-visible but tool-disabled", () => {
    assert.equal(BROWSER_METHOD_CATEGORY.vault_login, "credential_auth");
    assert.equal(BROWSER_METHOD_CATEGORY.vault_save, "credential_auth");
    assert.equal(BROWSER_METHOD_CATEGORY.save_state, "credential_auth");
    assert.equal(BROWSER_TOOL_METHODS.includes("vault_login"), false);
    assert.equal(BROWSER_TOOL_METHODS.includes("vault_save"), false);
    assert.equal(BROWSER_TOOL_METHODS.includes("save_state"), false);
  });

  it("includes inspection and artifact methods used by the ambient loop", () => {
    assert.equal(BROWSER_TOOL_METHODS.includes("navigate"), true);
    assert.equal(BROWSER_TOOL_METHODS.includes("snapshot"), true);
    assert.equal(BROWSER_TOOL_METHODS.includes("click_ref"), true);
    assert.equal(BROWSER_TOOL_METHODS.includes("visual_diff"), true);
  });
});
