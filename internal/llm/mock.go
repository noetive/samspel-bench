package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/goccy/go-json"
)

// MockCall is one tool call a scripted policy wants to make.
type MockCall struct {
	Name  string
	Input any
}

// MockObs is what a scripted policy sees before each turn.
type MockObs struct {
	Step  int
	Known string // system prompt plus every user turn so far
	Last  string // the latest user turn
	Tools map[string]bool
}

// MockPolicy decides the next tool calls from the observation.
type MockPolicy interface {
	Next(o MockObs) []MockCall
}

// Mock is a Model driven by a scripted policy. It exercises the whole harness
// (bus, faults, controls, verifiers, metrics) without API calls. Use one Mock
// per agent; it is not safe for concurrent use.
type Mock struct {
	Policy  MockPolicy
	Latency time.Duration
	step    int
	n       int
}

func (m *Mock) Complete(ctx context.Context, req Request) (*Response, error) {
	if err := sleep(ctx, m.Latency); err != nil {
		return nil, err
	}
	var all strings.Builder
	all.WriteString(req.System)
	all.WriteString("\n")
	last := ""
	for i, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}
		var sb strings.Builder
		for _, b := range msg.Content {
			sb.WriteString(b.Text)
			sb.WriteString(b.Content)
			sb.WriteString("\n")
		}
		all.WriteString(sb.String())
		if i == len(req.Messages)-1 {
			last = sb.String()
		}
	}
	tools := map[string]bool{}
	for _, t := range req.Tools {
		tools[t.Name] = true
	}
	calls := m.Policy.Next(MockObs{Step: m.step, Known: all.String(), Last: last, Tools: tools})
	m.step++
	resp := &Response{StopReason: "end_turn", Usage: Usage{InputTokens: all.Len() / 4, OutputTokens: 20}}
	if len(calls) == 0 {
		resp.Content = []Block{Text("(nothing to do)")}
		return resp, nil
	}
	for _, c := range calls {
		in, err := json.Marshal(c.Input)
		if err != nil {
			return nil, err
		}
		m.n++
		resp.Content = append(resp.Content, Block{Type: "tool_use", ID: fmt.Sprintf("toolu_mock_%d", m.n), Name: c.Name, Input: in})
	}
	resp.StopReason = "tool_use"
	return resp, nil
}
