package display

import "fmt"

// VerbosityLevel controls output density.
type VerbosityLevel int

const (
	Quiet   VerbosityLevel = iota // Connection status only
	Default                       // Compact tool calls, streamed text, one-line thinking
	Debug                         // Full thinking, tool results, input details
)

// ANSI escape codes
const (
	Reset   = "\033[0m"
	Bold    = "\033[1m"
	Dim     = "\033[2m"
	White   = "\033[37m"
	Cyan    = "\033[36m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Magenta = "\033[35m"
	Red     = "\033[31m"
	Gray    = "\033[90m"
	BgGreen = "\033[42m"
	BgRed   = "\033[41m"
	Black   = "\033[30m"
)

// HR returns a horizontal rule of the given character repeated n times.
func HR(char string, n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += char
	}
	return Dim + s + Reset
}

// Truncate shortens a string to max length, adding … if truncated.
func Truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// FormatRequestBanner prints a request start banner. No "TASK" label —
// the prompt text is the label.
func FormatRequestBanner(prompt, cwd, model string) string {
	lines := []string{HR("─", 46)}
	lines = append(lines, Truncate(prompt, 80))
	if cwd != "" {
		lines = append(lines, fmt.Sprintf("%scwd: %s%s", Dim, cwd, Reset))
	}
	if model != "" {
		lines = append(lines, fmt.Sprintf("%smodel: %s%s", Dim, model, Reset))
	}
	lines = append(lines, HR("─", 46))
	result := ""
	for _, l := range lines {
		result += l + "\n"
	}
	return result
}

// FormatErrorBanner prints a task failure banner.
func FormatErrorBanner(errMsg string) string {
	return fmt.Sprintf("\n%s%s%s FAILED %s %s\n", BgRed, Black, Bold, Reset, errMsg)
}
