package workflow

import (
	"encoding/json"
	"strings"
)

// extractTextDelta pulls text content from assistant and content_block_delta events.
func extractTextDelta(raw json.RawMessage) string {
	var deltaEvt struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(raw, &deltaEvt); err == nil && deltaEvt.Delta.Type == "text_delta" {
		return deltaEvt.Delta.Text
	}

	var assistantEvt struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &assistantEvt); err != nil {
		return ""
	}

	var out strings.Builder
	for _, item := range assistantEvt.Message.Content {
		if item.Type == "text" {
			out.WriteString(item.Text)
		}
	}
	return out.String()
}

// extractResultText pulls the final text from a result event.
func extractResultText(raw json.RawMessage) string {
	var evt struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		return ""
	}
	return evt.Result
}

// extractUsage pulls token and cost info from a result event.
// It returns input tokens, output tokens, and cost in ten-thousandths of USD.
func extractUsage(raw json.RawMessage) (int64, int64, int64) {
	var evt struct {
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
		CostUSD      float64 `json:"cost_usd"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		return 0, 0, 0
	}
	cost := evt.CostUSD
	if cost == 0 {
		cost = evt.TotalCostUSD
	}
	return evt.Usage.InputTokens, evt.Usage.OutputTokens, int64(cost * 10000)
}

// detectVerdict checks output for a PASS/FAIL verdict.
func detectVerdict(output string) string {
	var result struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(output), &result); err == nil {
		switch strings.ToLower(result.Verdict) {
		case "pass":
			return "pass"
		case "fail":
			return "fail"
		}
	}
	return "fail"
}
