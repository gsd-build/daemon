package workflow

import "regexp"

var portRefPattern = regexp.MustCompile(`\{([\w-]+):([\w-]+)\}`)

// ResolvePromptTemplate replaces {portName:nodeId} references with upstream
// node output values. Missing references resolve to empty string.
func ResolvePromptTemplate(template string, results map[string]map[string]string) string {
	return portRefPattern.ReplaceAllStringFunc(template, func(match string) string {
		groups := portRefPattern.FindStringSubmatch(match)
		if len(groups) != 3 {
			return match
		}
		portName, nodeID := groups[1], groups[2]
		ports, ok := results[nodeID]
		if !ok {
			return ""
		}
		return ports[portName]
	})
}
