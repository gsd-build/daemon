package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	protocol "github.com/gsd-build/protocol-go"
)

func TestStructuredQuestionRoundToProtocolQuestions(t *testing.T) {
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"id":            "scope",
				"header":        "Choose scope",
				"question":      "What should happen first?",
				"allowMultiple": true,
				"options": []any{
					map[string]any{"label": "Daemon", "description": "Bridge work", "preview": `{"file":"actor.go"}`},
				},
			},
		},
	}

	round, err := parseStructuredQuestionRound("toolu_123", args)
	if err != nil {
		t.Fatalf("parseStructuredQuestionRound: %v", err)
	}
	questions := round.toProtocolQuestions()
	if len(questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(questions))
	}
	if questions[0].RequestID != "toolu_123:scope" {
		t.Fatalf("RequestID = %q, want toolu_123:scope", questions[0].RequestID)
	}
	if questions[0].Header != "Choose scope" {
		t.Fatalf("Header = %q, want Choose scope", questions[0].Header)
	}
	if !questions[0].MultiSelect {
		t.Fatal("MultiSelect = false, want true")
	}
	if questions[0].Options[0].Preview != `{"file":"actor.go"}` {
		t.Fatalf("Preview = %q", questions[0].Options[0].Preview)
	}
}

func TestStructuredQuestionRoundRejectsInvalidPayloads(t *testing.T) {
	tests := map[string]map[string]any{
		"empty questions": {"questions": []any{}},
		"empty id": {
			"questions": []any{
				map[string]any{"id": "", "question": "Pick", "options": []any{map[string]any{"label": "A"}}},
			},
		},
		"empty options": {
			"questions": []any{
				map[string]any{"id": "scope", "question": "Pick", "options": []any{}},
			},
		},
		"duplicate ids": {
			"questions": []any{
				map[string]any{"id": "dup", "question": "Pick one", "options": []any{map[string]any{"label": "A"}}},
				map[string]any{"id": "dup", "question": "Pick two", "options": []any{map[string]any{"label": "B"}}},
			},
		},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseStructuredQuestionRound("toolu_123", args); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestStructuredQuestionRoundUsesQuestionForBlankHeader(t *testing.T) {
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"id":       "scope",
				"header":   "   ",
				"question": "Pick one",
				"options":  []any{map[string]any{"label": "A"}},
			},
		},
	}

	round, err := parseStructuredQuestionRound("toolu_123", args)
	if err != nil {
		t.Fatalf("parseStructuredQuestionRound: %v", err)
	}
	questions := round.toProtocolQuestions()
	if questions[0].Header != "Pick one" {
		t.Fatalf("Header = %q, want Pick one", questions[0].Header)
	}
}

func TestFormatStructuredQuestionResponse(t *testing.T) {
	round := structuredQuestionRound{
		ToolCallID: "toolu_123",
		Questions: []structuredQuestion{
			{ID: "scope", Question: "Pick", Options: []structuredQuestionOption{{Label: "Daemon"}}},
			{ID: "notes", Question: "Notes", Options: []structuredQuestionOption{{Label: "Web"}}},
		},
	}

	got := formatStructuredQuestionResponse(round, map[string]string{
		"toolu_123:scope": `["Daemon","user_note: Focus on bridge tests"]`,
		"toolu_123:notes": "Plain text",
	})

	var payload struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Answers["scope"].Answers[0] != "Daemon" {
		t.Fatalf("scope answer = %#v", payload.Answers["scope"].Answers)
	}
	if payload.Answers["notes"].Answers[0] != "Plain text" {
		t.Fatalf("notes answer = %#v", payload.Answers["notes"].Answers)
	}
}

func TestHandleStructuredQuestionRoundSendsAllQuestionsBeforeWaiting(t *testing.T) {
	relay := newFakeRelay()
	actor, err := NewActor(Options{
		SessionID: "sess-structured-batch",
		CWD:       t.TempDir(),
		Relay:     relay,
	})
	if err != nil {
		t.Fatalf("new actor: %v", err)
	}
	actor.interactionTimeout = time.Second

	round := structuredQuestionRound{
		ToolCallID: "toolu_structured",
		Questions: []structuredQuestion{
			{ID: "component", Header: "Component", Question: "Which component?", Options: []structuredQuestionOption{{Label: "Daemon"}, {Label: "Web"}, {Label: "Tests"}}},
			{ID: "mode", Header: "Mode", Question: "Which mode?", AllowMultiple: true, Options: []structuredQuestionOption{{Label: "Fast"}, {Label: "Safe"}, {Label: "Thorough"}}},
			{ID: "next", Header: "Next", Question: "What next?", Options: []structuredQuestionOption{{Label: "Ship"}, {Label: "Wait"}, {Label: "Investigate"}}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		response string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := actor.handleStructuredQuestionRound(ctx, ctx, &taskContext{ChannelID: "ch-structured"}, round)
		done <- result{response: response, err: err}
	}()

	if !relay.waitFor(t, time.Second, func(frames []any) bool {
		count := 0
		for _, frame := range frames {
			if _, ok := frame.(*protocol.Question); ok {
				count++
			}
		}
		return count == 3
	}) {
		t.Fatalf("structured round did not send all questions before waiting: got %d", countProtocolQuestions(relay.GetFrames()))
	}

	var questions []*protocol.Question
	for _, frame := range relay.GetFrames() {
		if question, ok := frame.(*protocol.Question); ok {
			questions = append(questions, question)
		}
	}

	for i := len(questions) - 1; i >= 0; i-- {
		question := questions[i]
		if err := actor.HandleQuestionResponse(&protocol.QuestionResponse{
			Type:      protocol.MsgTypeQuestionResponse,
			SessionID: "sess-structured-batch",
			ChannelID: "ch-structured",
			RequestID: question.RequestID,
			Answer:    `["answer-` + question.RequestID + `"]`,
		}); err != nil {
			t.Fatalf("HandleQuestionResponse %s: %v", question.RequestID, err)
		}
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("handleStructuredQuestionRound: %v", got.err)
		}
		var payload struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		}
		if err := json.Unmarshal([]byte(got.response), &payload); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		for _, id := range []string{"component", "mode", "next"} {
			if len(payload.Answers[id].Answers) != 1 {
				t.Fatalf("answer %s = %#v, want one answer", id, payload.Answers[id].Answers)
			}
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for structured question response")
	}
}

func countProtocolQuestions(frames []any) int {
	count := 0
	for _, frame := range frames {
		if _, ok := frame.(*protocol.Question); ok {
			count++
		}
	}
	return count
}

func TestParseStructuredQuestionRoundFromPlaceholder(t *testing.T) {
	payload := `{"questions":[{"id":"scope","question":"Pick one","options":[{"label":"Daemon"}]}]}`
	placeholder := structuredQuestionPlaceholderPrefix + base64.StdEncoding.EncodeToString([]byte(payload))

	round, ok := parseStructuredQuestionRoundFromPlaceholder("ui_123", placeholder)
	if !ok {
		t.Fatal("parseStructuredQuestionRoundFromPlaceholder returned false")
	}
	if round.ToolCallID != "ui_123" {
		t.Fatalf("ToolCallID = %q, want ui_123", round.ToolCallID)
	}
	if round.Questions[0].ID != "scope" {
		t.Fatalf("question ID = %q, want scope", round.Questions[0].ID)
	}
}

func TestStructuredQuestionCoordinatorPairsBothEventOrders(t *testing.T) {
	t.Run("tool start first", func(t *testing.T) {
		var c structuredQuestionCoordinator
		c.put(structuredQuestionRound{ToolCallID: "toolu_1"})
		round, ok := c.wait(context.Background())
		if !ok {
			t.Fatal("wait returned false")
		}
		if round.ToolCallID != "toolu_1" {
			t.Fatalf("ToolCallID = %q, want toolu_1", round.ToolCallID)
		}
	})

	t.Run("ui request first", func(t *testing.T) {
		var c structuredQuestionCoordinator
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		got := make(chan structuredQuestionRound, 1)
		go func() {
			round, ok := c.wait(ctx)
			if !ok {
				t.Errorf("wait returned false")
				return
			}
			got <- round
		}()
		c.put(structuredQuestionRound{ToolCallID: "toolu_2"})
		select {
		case round := <-got:
			if round.ToolCallID != "toolu_2" {
				t.Fatalf("ToolCallID = %q, want toolu_2", round.ToolCallID)
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for round")
		}
	})
}

func TestStructuredQuestionCoordinatorSkipsPlaceholderHandledRound(t *testing.T) {
	var c structuredQuestionCoordinator

	handled := structuredQuestionRound{
		ToolCallID: "ui_1",
		Questions: []structuredQuestion{
			{ID: "scope", Question: "Pick one", Options: []structuredQuestionOption{{Label: "Daemon"}}},
		},
	}
	next := structuredQuestionRound{
		ToolCallID: "toolu_2",
		Questions: []structuredQuestion{
			{ID: "texture", Question: "Pick texture", Options: []structuredQuestionOption{{Label: "Analog"}}},
		},
	}

	c.discardNextMatching(handled)
	c.put(structuredQuestionRound{
		ToolCallID: "toolu_1",
		Questions:  handled.Questions,
	})
	c.put(next)

	round, ok := c.wait(context.Background())
	if !ok {
		t.Fatal("wait returned false")
	}
	if round.ToolCallID != "toolu_2" {
		t.Fatalf("ToolCallID = %q, want toolu_2", round.ToolCallID)
	}
}
