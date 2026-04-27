package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	protocol "github.com/gsd-build/protocol-go"
)

const structuredQuestionPlaceholderPrefix = "__gsd_structured_questions__:"

type structuredQuestionRound struct {
	ToolCallID string
	Questions  []structuredQuestion
}

type structuredQuestion struct {
	ID            string                     `json:"id"`
	Header        string                     `json:"header"`
	Question      string                     `json:"question"`
	AllowMultiple bool                       `json:"allowMultiple"`
	Options       []structuredQuestionOption `json:"options"`
}

type structuredQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Preview     string `json:"preview"`
}

func parseStructuredQuestionRound(toolCallID string, args map[string]any) (structuredQuestionRound, error) {
	var payload struct {
		Questions []structuredQuestion `json:"questions"`
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return structuredQuestionRound{}, err
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return structuredQuestionRound{}, err
	}
	if len(payload.Questions) == 0 {
		return structuredQuestionRound{}, fmt.Errorf("structured question round has no questions")
	}
	for index, question := range payload.Questions {
		if strings.TrimSpace(question.ID) == "" {
			return structuredQuestionRound{}, fmt.Errorf("question %d has empty id", index)
		}
		if strings.TrimSpace(question.Question) == "" {
			return structuredQuestionRound{}, fmt.Errorf("question %s has empty question", question.ID)
		}
		if len(question.Options) == 0 {
			return structuredQuestionRound{}, fmt.Errorf("question %s has no options", question.ID)
		}
		for optionIndex, option := range question.Options {
			if strings.TrimSpace(option.Label) == "" {
				return structuredQuestionRound{}, fmt.Errorf("question %s option %d has empty label", question.ID, optionIndex)
			}
		}
	}
	return structuredQuestionRound{ToolCallID: toolCallID, Questions: payload.Questions}, nil
}

func parseStructuredQuestionRoundFromPlaceholder(toolCallID string, placeholder string) (structuredQuestionRound, bool) {
	if !strings.HasPrefix(placeholder, structuredQuestionPlaceholderPrefix) {
		return structuredQuestionRound{}, false
	}
	encoded := strings.TrimPrefix(placeholder, structuredQuestionPlaceholderPrefix)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return structuredQuestionRound{}, false
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return structuredQuestionRound{}, false
	}
	round, err := parseStructuredQuestionRound(toolCallID, args)
	if err != nil {
		return structuredQuestionRound{}, false
	}
	return round, true
}

func (round structuredQuestionRound) toProtocolQuestions() []protocol.Question {
	questions := make([]protocol.Question, 0, len(round.Questions))
	for _, question := range round.Questions {
		options := make([]protocol.QuestionOption, 0, len(question.Options))
		for _, option := range question.Options {
			options = append(options, protocol.QuestionOption{
				Label:       option.Label,
				Description: option.Description,
				Preview:     option.Preview,
			})
		}
		header := question.Header
		if header == "" {
			header = question.Question
		}
		questions = append(questions, protocol.Question{
			RequestID:   structuredQuestionRequestID(round.ToolCallID, question.ID),
			Header:      header,
			Question:    question.Question,
			MultiSelect: question.AllowMultiple,
			Options:     options,
		})
	}
	return questions
}

func structuredQuestionRequestID(toolCallID string, questionID string) string {
	return toolCallID + ":" + questionID
}

func formatStructuredQuestionResponse(round structuredQuestionRound, answers map[string]string) string {
	payload := map[string]any{"answers": map[string]any{}}
	answerMap := payload["answers"].(map[string]any)
	for _, question := range round.Questions {
		answerMap[question.ID] = map[string]any{
			"answers": parseAnswerList(answers[structuredQuestionRequestID(round.ToolCallID, question.ID)]),
		}
	}
	raw, _ := json.MarshalIndent(payload, "", "  ")
	return string(raw)
}

func parseAnswerList(answer string) []string {
	var parsed []string
	if err := json.Unmarshal([]byte(answer), &parsed); err == nil {
		return parsed
	}
	if strings.TrimSpace(answer) == "" {
		return nil
	}
	return []string{answer}
}

type structuredQuestionCoordinator struct {
	mu      sync.Mutex
	waiters []chan structuredQuestionRound
	pending []structuredQuestionRound
}

func (c *structuredQuestionCoordinator) put(round structuredQuestionRound) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.waiters) > 0 {
		waiter := c.waiters[0]
		c.waiters = c.waiters[1:]
		waiter <- round
		close(waiter)
		return
	}
	c.pending = append(c.pending, round)
}

func (c *structuredQuestionCoordinator) wait(ctx context.Context) (structuredQuestionRound, bool) {
	c.mu.Lock()
	if len(c.pending) > 0 {
		round := c.pending[0]
		c.pending = c.pending[1:]
		c.mu.Unlock()
		return round, true
	}
	waiter := make(chan structuredQuestionRound, 1)
	c.waiters = append(c.waiters, waiter)
	c.mu.Unlock()

	select {
	case round := <-waiter:
		return round, true
	case <-ctx.Done():
		c.mu.Lock()
		for i, candidate := range c.waiters {
			if candidate == waiter {
				c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
		return structuredQuestionRound{}, false
	}
}
