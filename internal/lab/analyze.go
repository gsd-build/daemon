package lab

import "fmt"

func AnalyzeBundle(bundle ExportBundle) (AnalysisReport, error) {
	if bundle.SchemaVersion != ExportSchemaVersion {
		return AnalysisReport{}, fmt.Errorf("unsupported provider lab export schema %d", bundle.SchemaVersion)
	}
	report := AnalysisReport{
		SchemaVersion: bundle.SchemaVersion,
		Provider:      bundle.Config.Provider,
		Model:         bundle.Config.Model,
		EventCount:    len(bundle.Events),
	}
	for _, event := range bundle.Events {
		switch event.Kind {
		case "task.error", "taskError":
			report.ErrorCount++
		case "tool.call":
			report.ToolCallCount++
		case "question":
			report.QuestionCount++
		case "terminal.opened", "terminal.output", "terminal.exit", "terminal.error", "terminalOpened", "terminalOutput", "terminalExit", "terminalError":
			report.TerminalCount++
		case "plan.request", "plan.response":
			report.PlanCallCount++
		}
	}
	return report, nil
}
