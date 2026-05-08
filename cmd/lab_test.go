package cmd

import "testing"

func TestParseLabBoolPtr(t *testing.T) {
	enabled, err := parseLabBoolPtr("on")
	if err != nil {
		t.Fatalf("parseLabBoolPtr(on): %v", err)
	}
	if enabled == nil || !*enabled {
		t.Fatalf("parseLabBoolPtr(on) = %v, want true", enabled)
	}
	disabled, err := parseLabBoolPtr(" false ")
	if err != nil {
		t.Fatalf("parseLabBoolPtr(false): %v", err)
	}
	if disabled == nil || *disabled {
		t.Fatalf("parseLabBoolPtr(false) = %v, want false", disabled)
	}
	empty, err := parseLabBoolPtr(" ")
	if err != nil {
		t.Fatalf("parseLabBoolPtr(empty): %v", err)
	}
	if empty != nil {
		t.Fatalf("parseLabBoolPtr(empty) = %v, want nil", *empty)
	}
	if got, err := parseLabBoolPtr("inherit"); err == nil || got != nil {
		t.Fatalf("parseLabBoolPtr(inherit) = %v, %v; want nil and error", got, err)
	}
}
