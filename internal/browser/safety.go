package browser

import "encoding/json"

type BrowserRisk string

const (
	BrowserRiskInspection         BrowserRisk = "inspection"
	BrowserRiskInteraction        BrowserRisk = "interaction"
	BrowserRiskExternalEffect     BrowserRisk = "external_effect"
	BrowserRiskNetworkMutation    BrowserRisk = "network_mutation"
	BrowserRiskCredentialAuth     BrowserRisk = "credential_auth"
	BrowserRiskArtifactGeneration BrowserRisk = "artifact_generation"
)

func classifyBrowserTool(method string, params json.RawMessage) BrowserRisk {
	switch method {
	case "click", "type", "press", "hover", "scroll", "select_option", "set_checked", "drag", "set_viewport", "click_ref", "hover_ref", "fill_ref", "emulate_device":
		return BrowserRiskInteraction
	case "eval", "fill_form", "act":
		return BrowserRiskExternalEffect
	case "mock_route", "block_urls", "clear_routes":
		return BrowserRiskNetworkMutation
	case "save_state", "restore_state", "vault_save", "vault_login", "vault_list":
		return BrowserRiskCredentialAuth
	case "upload_file", "debug_bundle", "screenshot", "zoom_region", "save_pdf", "visual_diff", "generate_test", "har_export", "trace_start", "trace_stop":
		return BrowserRiskArtifactGeneration
	case "batch":
		return classifyBrowserBatch(params)
	default:
		return BrowserRiskInspection
	}
}

func classifyBrowserBatch(params json.RawMessage) BrowserRisk {
	if len(params) == 0 {
		return BrowserRiskInspection
	}
	var payload struct {
		Steps []map[string]json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return BrowserRiskInspection
	}
	risk := BrowserRiskInspection
	for _, step := range payload.Steps {
		method := stringValue(step["method"])
		if method == "" {
			method = stringValue(step["action"])
		}
		if method == "" {
			continue
		}
		nested := classifyBrowserTool(method, stepPayload(step))
		if riskRank(nested) > riskRank(risk) {
			risk = nested
		}
	}
	return risk
}

func stringValue(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func stepPayload(step map[string]json.RawMessage) json.RawMessage {
	if raw := step["params"]; len(raw) > 0 {
		return raw
	}
	data, _ := json.Marshal(step)
	return data
}

func browserRiskRequiresApproval(risk BrowserRisk) bool {
	switch risk {
	case BrowserRiskExternalEffect, BrowserRiskNetworkMutation, BrowserRiskCredentialAuth:
		return true
	default:
		return false
	}
}

func riskRank(risk BrowserRisk) int {
	switch risk {
	case BrowserRiskCredentialAuth:
		return 5
	case BrowserRiskNetworkMutation:
		return 4
	case BrowserRiskExternalEffect:
		return 3
	case BrowserRiskArtifactGeneration:
		return 2
	case BrowserRiskInteraction:
		return 1
	default:
		return 0
	}
}
