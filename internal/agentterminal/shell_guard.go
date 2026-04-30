package agentterminal

import "strings"

func ShouldRunInBackground(command string) bool {
	normalized := strings.ToLower(strings.TrimSpace(command))
	if normalized == "" {
		return false
	}
	patterns := []string{
		"python3 -m http.server",
		"python -m http.server",
		"npm run dev",
		"pnpm dev",
		"yarn dev",
		"bun dev",
		"vite",
		"next dev",
		"astro dev",
		"remix dev",
		"rails server",
		"rails s",
		"python manage.py runserver",
		"tail -f",
		"watch ",
	}
	for _, pattern := range patterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	if strings.Contains(normalized, "docker compose up") && !strings.Contains(normalized, " -d") {
		return true
	}
	if strings.Contains(normalized, "--watch") || strings.Contains(normalized, "--serve") {
		return true
	}
	return false
}
