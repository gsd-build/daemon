package lab

type Scenario struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

func BuiltInScenarios() []Scenario {
	return []Scenario{
		{ID: "read-file", Label: "Read file", Prompt: "Read README.md and summarize it in one sentence."},
		{ID: "write-file", Label: "Write file", Prompt: "Create lab-output.txt with one sentence describing this provider lab run."},
		{ID: "edit-file", Label: "Edit file", Prompt: "Edit lab-output.txt by appending a second sentence."},
		{ID: "shell", Label: "Run shell", Prompt: "Run pwd and ls, then summarize the directory."},
		{ID: "ask-user-question", Label: "Ask user question", Prompt: "Use ask_user_question to ask which provider behavior to inspect."},
		{ID: "plan-mode", Label: "Plan Mode", Prompt: "Use Plan Mode tools to create a short implementation checklist."},
		{ID: "terminal", Label: "Terminal", Prompt: "Start a background terminal task that prints hello-lab."},
		{ID: "resume", Label: "Multi-turn resume", Prompt: "Remember the phrase LAB-RESUME-CHECK and ask me for the next instruction."},
	}
}
