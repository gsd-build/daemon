package agentterminal

import "testing"

func TestShouldRunInBackground(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"python3 -m http.server 8765", true},
		{"pnpm dev", true},
		{"docker compose up", true},
		{"docker compose up -d", false},
		{"go test ./...", false},
		{"npm test -- --watch", true},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			if got := ShouldRunInBackground(tc.command); got != tc.want {
				t.Fatalf("ShouldRunInBackground(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
