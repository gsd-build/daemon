package cmd

import "testing"

func TestParseLabBoolPtr(t *testing.T) {
	enabled := parseLabBoolPtr("on")
	if enabled == nil || !*enabled {
		t.Fatalf("parseLabBoolPtr(on) = %v, want true", enabled)
	}
	disabled := parseLabBoolPtr(" false ")
	if disabled == nil || *disabled {
		t.Fatalf("parseLabBoolPtr(false) = %v, want false", disabled)
	}
	if got := parseLabBoolPtr("inherit"); got != nil {
		t.Fatalf("parseLabBoolPtr(inherit) = %v, want nil", *got)
	}
}
