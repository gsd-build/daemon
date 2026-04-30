package agentterminal

import "strings"

func ShouldRunInBackground(command string) bool {
	tokens := shellTokens(command)
	if len(tokens) == 0 {
		return false
	}

	longRunning := [][]string{
		{"python3", "-m", "http.server"},
		{"python", "-m", "http.server"},
		{"npm", "run", "dev"},
		{"pnpm", "dev"},
		{"yarn", "dev"},
		{"bun", "dev"},
		{"next", "dev"},
		{"astro", "dev"},
		{"remix", "dev"},
		{"rails", "server"},
		{"rails", "s"},
		{"python", "manage.py", "runserver"},
		{"tail", "-f"},
	}
	for _, seq := range longRunning {
		if containsTokenSequence(tokens, seq) {
			return true
		}
	}
	for _, token := range tokens {
		if token == "vite" || token == "watch" || token == "--watch" || strings.HasPrefix(token, "--watch=") || token == "--serve" || strings.HasPrefix(token, "--serve=") {
			return true
		}
	}
	if dockerComposeUp(tokens) && !hasToken(tokens, "-d") && !hasToken(tokens, "--detach") {
		return true
	}
	return false
}

func shellTokens(command string) []string {
	var tokens []string
	var b strings.Builder
	var quote rune
	escaped := false
	for _, r := range strings.TrimSpace(command) {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			if b.Len() > 0 {
				tokens = append(tokens, strings.ToLower(b.String()))
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		b.WriteRune('\\')
	}
	if b.Len() > 0 {
		tokens = append(tokens, strings.ToLower(b.String()))
	}
	return tokens
}

func containsTokenSequence(tokens []string, seq []string) bool {
	if len(seq) == 0 || len(seq) > len(tokens) {
		return false
	}
	for i := 0; i <= len(tokens)-len(seq); i++ {
		matched := true
		for j := range seq {
			if tokens[i+j] != seq[j] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func dockerComposeUp(tokens []string) bool {
	return containsTokenSequence(tokens, []string{"docker", "compose", "up"}) || containsTokenSequence(tokens, []string{"docker-compose", "up"})
}

func hasToken(tokens []string, needle string) bool {
	for _, token := range tokens {
		if token == needle {
			return true
		}
	}
	return false
}
