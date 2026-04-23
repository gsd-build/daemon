package workflow

import "testing"

func TestReadyNodes(t *testing.T) {
	def := &Definition{
		Nodes: []NodeDef{
			{ID: "a", Type: "input"},
			{ID: "b", Type: "prompt"},
			{ID: "c", Type: "prompt"},
			{ID: "d", Type: "prompt"},
		},
		Edges: []EdgeDef{
			{Source: "a", Target: "b"},
			{Source: "a", Target: "c"},
			{Source: "b", Target: "d"},
			{Source: "c", Target: "d"},
		},
	}

	completed := map[string]bool{}
	running := map[string]bool{}
	ready := ReadyNodes(def, completed, running)
	if len(ready) != 1 || ready[0].ID != "a" {
		t.Errorf("expected [a], got %v", nodeIDs(ready))
	}

	completed["a"] = true
	ready = ReadyNodes(def, completed, running)
	ids := nodeIDs(ready)
	if len(ids) != 2 {
		t.Errorf("expected [b, c], got %v", ids)
	}

	completed["b"] = true
	running["c"] = true
	ready = ReadyNodes(def, completed, running)
	if len(ready) != 0 {
		t.Errorf("expected [], got %v", nodeIDs(ready))
	}

	completed["c"] = true
	delete(running, "c")
	ready = ReadyNodes(def, completed, running)
	if len(ready) != 1 || ready[0].ID != "d" {
		t.Errorf("expected [d], got %v", nodeIDs(ready))
	}
}

func nodeIDs(nodes []*NodeDef) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return ids
}
