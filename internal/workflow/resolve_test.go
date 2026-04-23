package workflow

import "testing"

func TestResolvePromptTemplate(t *testing.T) {
	results := map[string]map[string]string{
		"researcher": {"result": "Found 3 vulnerabilities."},
		"dir-node":   {"directory": "/home/user/project"},
	}

	tests := []struct {
		name     string
		template string
		want     string
	}{
		{
			name:     "simple reference",
			template: "Analyze this: {result:researcher}",
			want:     "Analyze this: Found 3 vulnerabilities.",
		},
		{
			name:     "multiple references",
			template: "{result:researcher}\n\nFix the code in {directory:dir-node}",
			want:     "Found 3 vulnerabilities.\n\nFix the code in /home/user/project",
		},
		{
			name:     "missing reference returns empty",
			template: "Check {result:nonexistent}",
			want:     "Check ",
		},
		{
			name:     "no references unchanged",
			template: "Plain text with {curly braces}",
			want:     "Plain text with {curly braces}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolvePromptTemplate(tt.template, results)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
