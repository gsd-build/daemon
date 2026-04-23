package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	msgTypeWorkflowStarted      = "workflowStarted"
	msgTypeWorkflowNodeStarted  = "workflowNodeStarted"
	msgTypeWorkflowNodeStream   = "workflowNodeStream"
	msgTypeWorkflowNodeComplete = "workflowNodeComplete"
	msgTypeWorkflowNodeError    = "workflowNodeError"
	msgTypeWorkflowComplete     = "workflowComplete"
	msgTypeWorkflowError        = "workflowError"
)

// RelaySender sends messages to the relay.
type RelaySender interface {
	Send(ctx context.Context, msg any) error
}

// Executor runs a workflow DAG on the local machine.
type Executor struct {
	def       *Definition
	cwd       string
	channelID string
	runID     string
	relay     RelaySender
	binary    string

	mu             sync.Mutex
	nodeResults    map[string]map[string]string
	nodeErrors     map[string]bool
	completed      map[string]bool
	running        map[string]bool
	processes      map[string]*exec.Cmd
	seq            int64
	cancelFn       context.CancelFunc
	totalInput     int64
	totalOutput    int64
	totalCostUnits int64
}

// NewExecutor creates a workflow executor.
func NewExecutor(def *Definition, cwd, channelID, runID string, relay RelaySender, binary string) *Executor {
	if binary == "" {
		binary = "claude"
	}
	return &Executor{
		def:         def,
		cwd:         cwd,
		channelID:   channelID,
		runID:       runID,
		relay:       relay,
		binary:      binary,
		nodeResults: make(map[string]map[string]string),
		nodeErrors:  make(map[string]bool),
		completed:   make(map[string]bool),
		running:     make(map[string]bool),
		processes:   make(map[string]*exec.Cmd),
	}
}

// Run executes the workflow DAG.
func (e *Executor) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.cancelFn = cancel
	e.mu.Unlock()
	defer cancel()

	startedAt := time.Now()
	e.send(ctx, workflowStartedMessage{
		Type:          msgTypeWorkflowStarted,
		WorkflowRunID: e.runID,
		ChannelID:     e.channelID,
		StartedAt:     startedAt.UTC().Format(time.RFC3339Nano),
	})

	for {
		if ctx.Err() != nil {
			e.send(context.Background(), workflowErrorMessage{
				Type:          msgTypeWorkflowError,
				WorkflowRunID: e.runID,
				ChannelID:     e.channelID,
				Error:         "workflow cancelled",
			})
			return
		}

		e.mu.Lock()
		ready := ReadyNodes(e.def, e.completed, e.running)
		allDone := len(e.completed)+len(e.nodeErrors) == len(e.def.Nodes)
		running := len(e.running)
		blocked := len(ready) == 0 && running == 0 && !allDone
		e.mu.Unlock()

		if allDone {
			break
		}
		if blocked {
			e.send(context.Background(), workflowErrorMessage{
				Type:          msgTypeWorkflowError,
				WorkflowRunID: e.runID,
				ChannelID:     e.channelID,
				Error:         "workflow blocked by failed or unsatisfied dependencies",
			})
			return
		}
		if len(ready) == 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		var wg sync.WaitGroup
		for _, node := range ready {
			e.mu.Lock()
			e.running[node.ID] = true
			e.mu.Unlock()

			wg.Add(1)
			go func(n *NodeDef) {
				defer wg.Done()
				e.executeNode(ctx, n)
			}(node)
		}
		wg.Wait()
	}

	dur := time.Since(startedAt)
	e.mu.Lock()
	errorCount := len(e.nodeErrors)
	e.mu.Unlock()
	if errorCount > 0 {
		e.send(context.Background(), workflowErrorMessage{
			Type:          msgTypeWorkflowError,
			WorkflowRunID: e.runID,
			ChannelID:     e.channelID,
			Error:         fmt.Sprintf("%d node(s) failed", errorCount),
		})
		return
	}

	e.send(context.Background(), workflowCompleteMessage{
		Type:              msgTypeWorkflowComplete,
		WorkflowRunID:     e.runID,
		ChannelID:         e.channelID,
		TotalInputTokens:  atomic.LoadInt64(&e.totalInput),
		TotalOutputTokens: atomic.LoadInt64(&e.totalOutput),
		TotalCostUSD:      fmt.Sprintf("%.4f", float64(atomic.LoadInt64(&e.totalCostUnits))/10000),
		DurationMs:        int(dur.Milliseconds()),
	})
}

// Stop cancels a running workflow.
func (e *Executor) Stop() {
	e.mu.Lock()
	cancel := e.cancelFn
	for key, cmd := range e.processes {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		delete(e.processes, key)
	}
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (e *Executor) executeNode(ctx context.Context, node *NodeDef) {
	startedAt := time.Now()
	e.send(ctx, workflowNodeStartedMessage{
		Type:          msgTypeWorkflowNodeStarted,
		WorkflowRunID: e.runID,
		ChannelID:     e.channelID,
		NodeID:        node.ID,
		NodeLabel:     node.Label,
		StartedAt:     startedAt.UTC().Format(time.RFC3339Nano),
	})

	var result map[string]string
	var nodeErr error

	switch node.Type {
	case "input":
		result = map[string]string{"result": node.Content}
		if result["result"] == "" {
			result["result"] = node.Prompt
		}
	case "directory":
		result = map[string]string{"directory": node.DirectoryPath}
	case "script":
		result, nodeErr = e.executeScript(ctx, node)
	case "prompt":
		result, nodeErr = e.executeAgent(ctx, node)
	default:
		nodeErr = fmt.Errorf("unsupported node type: %s", node.Type)
	}

	e.mu.Lock()
	delete(e.running, node.ID)
	if nodeErr != nil {
		e.nodeErrors[node.ID] = true
	} else {
		e.completed[node.ID] = true
		e.nodeResults[node.ID] = result
	}
	e.mu.Unlock()

	dur := time.Since(startedAt)
	if nodeErr != nil {
		e.send(ctx, workflowNodeErrorMessage{
			Type:          msgTypeWorkflowNodeError,
			WorkflowRunID: e.runID,
			ChannelID:     e.channelID,
			NodeID:        node.ID,
			Error:         nodeErr.Error(),
		})
		return
	}

	e.send(ctx, workflowNodeCompleteMessage{
		Type:              msgTypeWorkflowNodeComplete,
		WorkflowRunID:     e.runID,
		ChannelID:         e.channelID,
		NodeID:            node.ID,
		OutputPortResults: result,
		DurationMs:        int(dur.Milliseconds()),
	})
}

func (e *Executor) executeScript(ctx context.Context, node *NodeDef) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", node.Command)
	cmd.Dir = e.workingDir()

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("script failed: %w\n%s", err, string(out))
	}
	return map[string]string{"result": string(out)}, nil
}

func (e *Executor) executeAgent(ctx context.Context, node *NodeDef) (map[string]string, error) {
	e.mu.Lock()
	prompt := ResolvePromptTemplate(node.Prompt, e.nodeResults)
	e.mu.Unlock()

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}

	model := node.Model
	if model == "" {
		model = "sonnet"
	}
	args = append(args, "--model", model)

	if node.Effort != "" {
		args = append(args, "--effort", node.Effort)
	}
	if node.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", node.SystemPrompt)
	}
	if len(node.Tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(node.Tools, ","))
	}

	for _, edge := range e.def.Edges {
		if edge.Target != node.ID || edge.TargetPort != "directory" {
			continue
		}
		e.mu.Lock()
		ports := e.nodeResults[edge.Source]
		dir := ports["directory"]
		e.mu.Unlock()
		if dir != "" {
			args = append(args, "--add-dir", dir)
		}
	}

	args = append(args, "--", prompt)

	cmd := exec.CommandContext(ctx, e.binary, args...)
	cmd.Dir = e.workingDir()

	key := e.runID + ":" + node.ID
	e.mu.Lock()
	e.processes[key] = cmd
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.processes, key)
		e.mu.Unlock()
	}()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	var fullText strings.Builder
	var resultRaw json.RawMessage

	scanner := NewNDJSONScanner(stdout)
	for scanner.Scan() {
		event := scanner.Event()
		seq := atomic.AddInt64(&e.seq, 1)
		e.send(ctx, workflowNodeStreamMessage{
			Type:           msgTypeWorkflowNodeStream,
			WorkflowRunID:  e.runID,
			ChannelID:      e.channelID,
			NodeID:         node.ID,
			SequenceNumber: seq,
			Event:          event.Raw,
		})

		if event.Type == "assistant" || event.Type == "content_block_delta" {
			if text := extractTextDelta(event.Raw); text != "" {
				fullText.WriteString(text)
			}
		}
		if event.Type == "result" {
			resultRaw = make(json.RawMessage, len(event.Raw))
			copy(resultRaw, event.Raw)
		}
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("claude exited: %w", err)
	}

	output := fullText.String()
	if resultRaw != nil {
		if text := extractResultText(resultRaw); text != "" {
			output = text
		}
		inTok, outTok, cost := extractUsage(resultRaw)
		atomic.AddInt64(&e.totalInput, inTok)
		atomic.AddInt64(&e.totalOutput, outTok)
		atomic.AddInt64(&e.totalCostUnits, cost)
	}

	portResults := map[string]string{"result": output}
	if hasConditionalPorts(node) {
		if detectVerdict(output) == "pass" {
			portResults["yes"] = output
		} else {
			portResults["no"] = output
		}
	}
	return portResults, nil
}

func (e *Executor) workingDir() string {
	if e.def.WorkingDir != "" {
		return e.def.WorkingDir
	}
	return e.cwd
}

func (e *Executor) send(ctx context.Context, msg any) {
	if e.relay == nil {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := e.relay.Send(sendCtx, msg); err != nil {
		slog.Warn("workflow: relay send failed", "err", err)
	}
}

func hasConditionalPorts(node *NodeDef) bool {
	for _, port := range node.OutputPorts {
		if port.Name == "yes" || port.Name == "no" {
			return true
		}
	}
	return false
}

type workflowStartedMessage struct {
	Type          string `json:"type"`
	WorkflowRunID string `json:"workflowRunId"`
	ChannelID     string `json:"channelId"`
	StartedAt     string `json:"startedAt"`
}

type workflowNodeStartedMessage struct {
	Type          string `json:"type"`
	WorkflowRunID string `json:"workflowRunId"`
	ChannelID     string `json:"channelId"`
	NodeID        string `json:"nodeId"`
	NodeLabel     string `json:"nodeLabel"`
	StartedAt     string `json:"startedAt"`
}

type workflowNodeStreamMessage struct {
	Type           string          `json:"type"`
	WorkflowRunID  string          `json:"workflowRunId"`
	ChannelID      string          `json:"channelId"`
	NodeID         string          `json:"nodeId"`
	SequenceNumber int64           `json:"sequenceNumber"`
	Event          json.RawMessage `json:"event"`
}

type workflowNodeCompleteMessage struct {
	Type              string            `json:"type"`
	WorkflowRunID     string            `json:"workflowRunId"`
	ChannelID         string            `json:"channelId"`
	NodeID            string            `json:"nodeId"`
	OutputPortResults map[string]string `json:"outputPortResults"`
	DurationMs        int               `json:"durationMs"`
}

type workflowNodeErrorMessage struct {
	Type          string `json:"type"`
	WorkflowRunID string `json:"workflowRunId"`
	ChannelID     string `json:"channelId"`
	NodeID        string `json:"nodeId"`
	Error         string `json:"error"`
}

type workflowCompleteMessage struct {
	Type              string `json:"type"`
	WorkflowRunID     string `json:"workflowRunId"`
	ChannelID         string `json:"channelId"`
	TotalInputTokens  int64  `json:"totalInputTokens"`
	TotalOutputTokens int64  `json:"totalOutputTokens"`
	TotalCostUSD      string `json:"totalCostUsd"`
	DurationMs        int    `json:"durationMs"`
}

type workflowErrorMessage struct {
	Type          string `json:"type"`
	WorkflowRunID string `json:"workflowRunId"`
	ChannelID     string `json:"channelId"`
	Error         string `json:"error"`
}
