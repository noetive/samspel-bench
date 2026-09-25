// Package llm holds the model interface, the Anthropic Messages API client,
// a shared rate limiter and a scripted mock model for harness tests.
package llm

import (
	"errors"

	"context"
	"time"

	"github.com/tidwall/gjson"

	"github.com/goccy/go-json"
)

// CacheControl marks a prompt-caching breakpoint.
type CacheControl struct {
	Type string `json:"type"`
}

// Block is one content block of a Messages API message.
type Block struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
}

// Text returns a text block.
func Text(s string) Block { return Block{Type: "text", Text: s} }

// ToolResult returns a tool_result block answering the tool_use with id.
func ToolResult(id, content string, isErr bool) Block {
	if content == "" {
		content = "(empty)"
	}
	return Block{Type: "tool_result", ToolUseID: id, Content: content, IsError: isErr}
}

// Message is one turn of a conversation.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Tool is a tool definition offered to the model.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Request is one model call.
type Request struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []Tool
	MaxTokens   int
	Temperature *float64
}

// Usage is token accounting as reported by the API.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Add accumulates o into u.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheCreationInputTokens += o.CacheCreationInputTokens
	u.CacheReadInputTokens += o.CacheReadInputTokens
}

// Total counts every input and output token, cached or not.
func (u Usage) Total() int {
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// Response is a model reply.
type Response struct {
	Content    []Block `json:"content"`
	StopReason string  `json:"stop_reason"`
	Usage      Usage   `json:"usage"`
}

// Model is anything that completes a request.
type Model interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ParseInput reads a model-written tool input. The whole input must be valid
// JSON before any field is read, so a malformed call is refused rather than
// half-read. Fields are then read by exact name.
func ParseInput(input json.RawMessage) (gjson.Result, error) {
	if !gjson.ValidBytes(input) {
		return gjson.Result{}, errors.New("tool input is not valid JSON")
	}
	return gjson.ParseBytes(input), nil
}
