// Package trace is the append-only record of one run. Every metric is
// computed from it, and it can be written out for replay and inspection.
package trace

import (
	"bufio"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/noetive/samspel-bench/internal/llm"
)

// Event kinds: send, deliver, drop, duplicate, reject, tool, model, flag.
type Event struct {
	AtMS   int64      `json:"at_ms"`
	Kind   string     `json:"kind"`
	Agent  string     `json:"agent,omitempty"`
	To     string     `json:"to,omitempty"`
	MsgID  int64      `json:"msg_id,omitempty"`
	Text   string     `json:"text,omitempty"`
	Tool   string     `json:"tool,omitempty"`
	Input  string     `json:"input,omitempty"`
	Result string     `json:"result,omitempty"`
	Usage  *llm.Usage `json:"usage,omitempty"`
}

// Trace is safe for concurrent use.
type Trace struct {
	mu     sync.Mutex
	start  time.Time
	events []Event
}

func New() *Trace { return &Trace{start: time.Now()} }

func (t *Trace) Add(e Event) {
	t.mu.Lock()
	e.AtMS = time.Since(t.start).Milliseconds()
	t.events = append(t.events, e)
	t.mu.Unlock()
}

// Events returns a copy of all events so far.
func (t *Trace) Events() []Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Event(nil), t.events...)
}

// SentBy concatenates the text of every message the agent sent, as sent,
// before any channel fault.
func (t *Trace) SentBy(agent string) string {
	var sb strings.Builder
	for _, e := range t.Events() {
		if e.Kind == "send" && e.Agent == agent {
			sb.WriteString(e.Text)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// ToolInputsBy concatenates the inputs of the agent's task tool calls
// (everything except send and broadcast).
func (t *Trace) ToolInputsBy(agent string) string {
	var sb strings.Builder
	for _, e := range t.Events() {
		if e.Kind == "tool" && e.Agent == agent && e.Tool != "send" && e.Tool != "broadcast" {
			sb.WriteString(e.Input)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func (t *Trace) WriteJSONL(path string) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range t.Events() {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return w.Flush()
}
