package display

import (
	"strings"
	"testing"
)

func TestTruncateShortString(t *testing.T) {
	got := Truncate("hello", 10)
	if got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestTruncateLongString(t *testing.T) {
	got := Truncate("hello world this is long", 10)
	if len([]rune(got)) > 11 { // 10 + ellipsis
		t.Errorf("expected truncated string, got %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
}

func TestTruncateUnicode(t *testing.T) {
	got := Truncate("日本語テスト文字列データ", 5)
	runes := []rune(got)
	if len(runes) > 6 { // 5 + ellipsis
		t.Errorf("expected 6 runes max, got %d: %q", len(runes), got)
	}
}

func TestHR(t *testing.T) {
	got := HR("─", 5)
	if !strings.Contains(got, "─────") {
		t.Errorf("expected 5 dashes, got %q", got)
	}
	if !strings.Contains(got, Dim) {
		t.Errorf("expected dim ANSI code")
	}
}

func TestFormatRequestBanner(t *testing.T) {
	got := FormatRequestBanner("Fix the login bug", "/home/user/project", "claude-sonnet-4-5-20250514")
	if !strings.Contains(got, "Fix the login bug") {
		t.Error("banner should contain prompt text")
	}
	if !strings.Contains(got, "cwd:") {
		t.Error("banner should contain cwd")
	}
	if !strings.Contains(got, "model:") {
		t.Error("banner should contain model")
	}
	if strings.Contains(got, "TASK") {
		t.Error("banner should not contain TASK label")
	}
}

func TestFormatRequestBannerTruncatesLongPrompt(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := FormatRequestBanner(long, "", "")
	if strings.Contains(got, long) {
		t.Error("long prompt should be truncated")
	}
}

func TestFormatErrorBanner(t *testing.T) {
	got := FormatErrorBanner("claude exited with code 1")
	if !strings.Contains(got, "FAILED") {
		t.Error("error banner should contain FAILED")
	}
	if !strings.Contains(got, "claude exited with code 1") {
		t.Error("error banner should contain error message")
	}
}
