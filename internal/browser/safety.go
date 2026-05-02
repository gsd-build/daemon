package browser

import "encoding/json"

type BrowserRisk string

const (
	BrowserRiskInspection          BrowserRisk = "inspection"
	BrowserRiskInteraction         BrowserRisk = "interaction"
	BrowserRiskExternalEffect      BrowserRisk = "external_effect"
	BrowserRiskNetworkMutation     BrowserRisk = "network_mutation"
	BrowserRiskCredentialAuth      BrowserRisk = "credential_auth"
	BrowserRiskArtifactGeneration  BrowserRisk = "artifact_generation"
	BrowserRiskModelVisibleCapture BrowserRisk = "model_visible_capture"
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
	case "screenshot", "snapshot", "page_source", "dom_extract", "console_read", "network_read", "artifact_create":
		return BrowserRiskModelVisibleCapture
	case "upload_file", "debug_bundle", "zoom_region", "save_pdf", "visual_diff", "generate_test", "har_export", "trace_start", "trace_stop":
		return BrowserRiskArtifactGeneration
	case "batch":
		return classifyBrowserBatch(params)
	default:
		return BrowserRiskInspection
	}
}

func validateBrowserToolPolicy(method string, params json.RawMessage) error {
	if method != "upload_file" {
		return nil
	}
	var payload struct {
		FileToken       string `json:"fileToken"`
		SelectedToken   string `json:"selectedFileToken"`
		AllowlistedPath string `json:"allowlistedPath"`
	}
	_ = json.Unmarshal(params, &payload)
	if validLexSelectedFileToken(payload.FileToken) || validLexSelectedFileToken(payload.SelectedToken) {
		return nil
	}
	return ErrBrowserPolicy("upload_file requires a Lex-selected file token")
}

func validLexSelectedFileToken(token string) bool {
	const prefix = "lex_file_"
	if len(token) < len(prefix)+16 || token[:len(prefix)] != prefix {
		return false
	}
	for _, r := range token[len(prefix):] {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

type ErrBrowserPolicy string

func (e ErrBrowserPolicy) Error() string {
	return string(e)
}

func classifyBrowserBatch(params json.RawMessage) BrowserRisk {
	if len(params) == 0 {
		return BrowserRiskInspection
	}
	var payload struct {
		Steps []map[string]json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return BrowserRiskExternalEffect
	}
	risk := BrowserRiskInspection
	for _, step := range payload.Steps {
		method := stringValue(step["method"])
		if method == "" {
			method = stringValue(step["action"])
		}
		nested := BrowserRiskExternalEffect
		if method != "" {
			nested = classifyBrowserTool(method, stepPayload(step))
		}
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
	case BrowserRiskModelVisibleCapture:
		return 2
	case BrowserRiskArtifactGeneration:
		return 2
	case BrowserRiskInteraction:
		return 1
	default:
		return 0
	}
}
