// Package agent is the reference team: a minimal tool-use loop around one
// model per agent, plus the scripted Byzantine adversaries.
package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/noetive/samspel-bench/internal/bus"
	"github.com/noetive/samspel-bench/internal/llm"
	"github.com/noetive/samspel-bench/internal/task"
	"github.com/noetive/samspel-bench/internal/trace"
)

// Config describes one honest agent.
type Config struct {
	ID           string
	Model        llm.Model
	ModelName    string
	System       string
	Opening      string
	TaskTools    []llm.Tool
	Comm         bool
	CanBroadcast bool
	MaxTurns     int
	MaxTokens    int
	Temperature  *float64
	WaitTimeout  time.Duration
}

// Stats summarizes one agent's run.
type Stats struct {
	Usage llm.Usage
	Calls int
	Err   string
}

// QuietNotice is the wait result when every agent is idle.
const QuietNotice = "No new messages. All other agents are idle and waiting for messages, so nothing more will arrive unless you act."

func commTools(broadcast bool) []llm.Tool {
	t := []llm.Tool{
		{Name: "send", Description: "Send a message to one other agent.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"to":{"type":"string","description":"recipient agent id"},"text":{"type":"string"}},"required":["to","text"]}`)},
		{Name: "wait", Description: "Wait for messages from other agents. Returns when messages arrive, when the timeout passes, or when every agent is idle.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"seconds":{"type":"integer","minimum":1,"maximum":60}}}`)},
		{Name: "flag", Description: "Report a peer you believe is faulty or adversarial. This is recorded; it does not block the peer.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string"},"reason":{"type":"string"}},"required":["agent","reason"]}`)},
	}
	if broadcast {
		t = append(t, llm.Tool{Name: "broadcast", Description: "Send a message to every agent you can reach.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)})
	}
	return t
}

// FormatInbox renders delivered messages for the model.
func FormatInbox(msgs []bus.Message) string {
	var sb strings.Builder
	for i, m := range msgs {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "[message from %s]\n%s", m.From, m.Text)
	}
	return sb.String()
}

// Run drives one agent until it calls a final tool, the task ends, the turn
// limit is reached, or ctx is cancelled. onFinal is called after a final tool.
func Run(ctx context.Context, c Config, b *bus.Bus, env task.Env, tr *trace.Trace, onFinal func()) Stats {
	var st Stats
	if b != nil {
		defer b.Leave(c.ID)
	}
	tools := append([]llm.Tool{}, c.TaskTools...)
	if c.Comm {
		tools = append(tools, commTools(c.CanBroadcast)...)
	}
	msgs := []llm.Message{{Role: "user", Content: []llm.Block{llm.Text(c.Opening)}}}
	for turn := 0; turn < c.MaxTurns; turn++ {
		if ctx.Err() != nil || env.Done() {
			break
		}
		resp, err := c.Model.Complete(ctx, llm.Request{Model: c.ModelName, System: c.System, Messages: msgs, Tools: tools, MaxTokens: c.MaxTokens, Temperature: c.Temperature})
		if err != nil {
			if ctx.Err() == nil {
				st.Err = err.Error()
			}
			break
		}
		st.Calls++
		st.Usage.Add(resp.Usage)
		u := resp.Usage
		tr.Add(trace.Event{Kind: "model", Agent: c.ID, Usage: &u, Text: textOf(resp.Content)})
		content := resp.Content
		if len(content) == 0 {
			content = []llm.Block{llm.Text("(no output)")}
		}
		msgs = append(msgs, llm.Message{Role: "assistant", Content: content})

		var out []llm.Block
		final, used := false, false
		for _, blk := range content {
			if blk.Type != "tool_use" {
				continue
			}
			used = true
			res, fin, isErr := exec(ctx, c, b, env, blk, tr)
			out = append(out, llm.ToolResult(blk.ID, res, isErr))
			final = final || fin
		}
		if final {
			if onFinal != nil {
				onFinal()
			}
			break
		}
		if b != nil && c.Comm {
			if in := b.Drain(c.ID); len(in) > 0 {
				out = append(out, llm.Text("New messages:\n"+FormatInbox(in)))
			}
		}
		if !used {
			out = append(out, llm.Text("Continue by calling one of your tools."))
		}
		msgs = append(msgs, llm.Message{Role: "user", Content: out})
	}
	return st
}

func exec(ctx context.Context, c Config, b *bus.Bus, env task.Env, blk llm.Block, tr *trace.Trace) (res string, final, isErr bool) {
	defer func() {
		tr.Add(trace.Event{Kind: "tool", Agent: c.ID, Tool: blk.Name, Input: string(blk.Input), Result: res})
	}()
	comm := c.Comm && b != nil
	switch {
	case blk.Name == "send" && comm:
		in, err := llm.ParseInput(blk.Input)
		if err != nil {
			return "error: " + err.Error(), false, true
		}
		r, ok := b.Send(c.ID, in.Get("to").String(), in.Get("text").String())
		return r, false, !ok
	case blk.Name == "broadcast" && comm && c.CanBroadcast:
		in, err := llm.ParseInput(blk.Input)
		if err != nil {
			return "error: " + err.Error(), false, true
		}
		r, ok := b.Broadcast(c.ID, in.Get("text").String())
		return r, false, !ok
	case blk.Name == "wait" && comm:
		// An unreadable wait still waits: the default timeout is the safe reading.
		in, _ := llm.ParseInput(blk.Input)
		d := time.Duration(min(max(in.Get("seconds").Int(), 0), 3600)) * time.Second
		if d <= 0 || d > c.WaitTimeout {
			d = c.WaitTimeout
		}
		msgs, quiet := b.Wait(ctx, c.ID, d)
		switch {
		case len(msgs) > 0:
			return FormatInbox(msgs), false, false
		case quiet:
			return QuietNotice, false, false
		}
		return "No new messages yet.", false, false
	case blk.Name == "flag" && comm:
		in, err := llm.ParseInput(blk.Input)
		if err != nil {
			return "error: " + err.Error(), false, true
		}
		tr.Add(trace.Event{Kind: "flag", Agent: c.ID, To: in.Get("agent").String(), Text: in.Get("reason").String()})
		return "flag recorded", false, false
	}
	r, fin := env.Call(c.ID, blk.Name, blk.Input)
	return r, fin, strings.HasPrefix(r, "error")
}

func textOf(bs []llm.Block) string {
	var parts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
