package loop

import "testing"

func TestSanitizeSessionTitle(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "removes quotes and ending punctuation",
			input: `"AI Chat Titles!"`,
			want:  "AI Chat Titles",
		},
		{
			name:  "collapses whitespace",
			input: "  AI\n\nChat\tTitles  ",
			want:  "AI Chat Titles",
		},
		{
			name:  "limits length at a word boundary",
			input: "This generated title is intentionally much longer than the title budget allows",
			want:  "This generated title is intentionally much longer than the",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeSessionTitle(tc.input)
			if got != tc.want {
				t.Fatalf("sanitizeSessionTitle(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
