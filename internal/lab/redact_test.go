package lab

import "testing"

func TestRedactorRedactsSecretLikeValues(t *testing.T) {
	redactor := DefaultRedactor()
	value, records := redactor.Redact("headers.authorization", "Bearer sk-secret-value")
	if value == "Bearer sk-secret-value" {
		t.Fatalf("secret value was not redacted")
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Path != "headers.authorization" {
		t.Fatalf("path = %q", records[0].Path)
	}
	if records[0].Hash == "" {
		t.Fatal("expected hash")
	}
}

func TestRedactorLeavesNormalText(t *testing.T) {
	redactor := DefaultRedactor()
	value, records := redactor.Redact("message.text", "hello world")
	if value != "hello world" {
		t.Fatalf("value = %q", value)
	}
	if len(records) != 0 {
		t.Fatalf("records = %d, want 0", len(records))
	}
}
