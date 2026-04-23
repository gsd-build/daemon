package workflow

import "encoding/json"

// PortDef is a named input or output on a node.
type PortDef struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// NodeDef is one node in a workflow definition.
type NodeDef struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Position struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"position"`
	InputPorts    []PortDef `json:"inputPorts"`
	OutputPorts   []PortDef `json:"outputPorts"`
	Prompt        string    `json:"prompt,omitempty"`
	SystemPrompt  string    `json:"systemPrompt,omitempty"`
	Model         string    `json:"model,omitempty"`
	Tools         []string  `json:"tools,omitempty"`
	Effort        string    `json:"effort,omitempty"`
	Command       string    `json:"command,omitempty"`
	Content       string    `json:"content,omitempty"`
	DirectoryPath string    `json:"directoryPath,omitempty"`
}

// EdgeDef is one edge connecting an output port to an input port.
type EdgeDef struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	SourcePort string `json:"sourcePort,omitempty"`
	TargetPort string `json:"targetPort,omitempty"`
}

// Definition is the full workflow graph.
type Definition struct {
	SchemaVersion int       `json:"schemaVersion"`
	Name          string    `json:"name"`
	WorkingDir    string    `json:"workingDirectory,omitempty"`
	Nodes         []NodeDef `json:"nodes"`
	Edges         []EdgeDef `json:"edges"`
}

// ParseDefinition decodes a workflow definition from JSON.
func ParseDefinition(data json.RawMessage) (*Definition, error) {
	var def Definition
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, err
	}
	return &def, nil
}

// NodesByID returns a map from node ID to node definition.
func (d *Definition) NodesByID() map[string]*NodeDef {
	m := make(map[string]*NodeDef, len(d.Nodes))
	for i := range d.Nodes {
		m[d.Nodes[i].ID] = &d.Nodes[i]
	}
	return m
}

// Upstream returns the IDs of all nodes that have an edge targeting the given node.
func (d *Definition) Upstream(nodeID string) []string {
	var ids []string
	for _, e := range d.Edges {
		if e.Target == nodeID {
			ids = append(ids, e.Source)
		}
	}
	return ids
}
