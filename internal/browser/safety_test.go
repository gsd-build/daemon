package browser

import (
	"encoding/json"
	"testing"
)

func TestClassifyBrowserToolIgnoresForgedSafeCategory(t *testing.T) {
	risk := classifyBrowserTool("fill_form", json.RawMessage(`{"category":"inspection"}`))
	if risk != BrowserRiskExternalEffect {
		t.Fatalf("risk = %s, want %s", risk, BrowserRiskExternalEffect)
	}
}

func TestClassifyBrowserBatchRecursesIntoNestedBatch(t *testing.T) {
	risk := classifyBrowserTool("batch", json.RawMessage(`{
		"steps": [
			{"method": "snapshot"},
			{"method": "batch", "params": {"steps": [{"method": "vault_login"}]}}
		]
	}`))
	if risk != BrowserRiskCredentialAuth {
		t.Fatalf("risk = %s, want %s", risk, BrowserRiskCredentialAuth)
	}
}

func TestUploadRequiresLexSelectedFileToken(t *testing.T) {
	if err := validateBrowserToolPolicy("upload_file", json.RawMessage(`{"path":"/tmp/secret.txt"}`)); err == nil {
		t.Fatal("expected upload without file token to fail")
	}
	if err := validateBrowserToolPolicy("upload_file", json.RawMessage(`{"fileToken":"lex_file_0123456789abcdef"}`)); err != nil {
		t.Fatalf("upload with file token: %v", err)
	}
	if err := validateBrowserToolPolicy("upload_file", json.RawMessage(`{"allowlistedPath":"/tmp/secret.txt"}`)); err == nil {
		t.Fatal("expected upload with path-only provenance to fail")
	}
	if err := validateBrowserToolPolicy("upload_file", json.RawMessage(`{"selectedFileToken":"agent_forged_0123456789abcdef"}`)); err == nil {
		t.Fatal("expected upload with forged file token to fail")
	}
}

func TestModelVisibleCaptureRisk(t *testing.T) {
	for _, method := range []string{"screenshot", "snapshot", "page_source", "dom_extract", "console_read", "network_read", "artifact_create"} {
		if risk := classifyBrowserTool(method, nil); risk != BrowserRiskModelVisibleCapture {
			t.Fatalf("%s risk = %s, want %s", method, risk, BrowserRiskModelVisibleCapture)
		}
	}
}
