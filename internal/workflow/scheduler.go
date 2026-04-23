package workflow

// ReadyNodes returns nodes whose upstream dependencies are all completed and
// that are not already completed or running.
func ReadyNodes(def *Definition, completed, running map[string]bool) []*NodeDef {
	var ready []*NodeDef
	for i := range def.Nodes {
		node := &def.Nodes[i]
		if completed[node.ID] || running[node.ID] {
			continue
		}

		allDone := true
		for _, upstreamID := range def.Upstream(node.ID) {
			if !completed[upstreamID] {
				allDone = false
				break
			}
		}
		if allDone {
			ready = append(ready, node)
		}
	}
	return ready
}
