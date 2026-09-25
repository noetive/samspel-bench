package agent

import (
	"context"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/noetive/samspel-bench/internal/bus"
	"github.com/noetive/samspel-bench/internal/llm"
	"github.com/noetive/samspel-bench/internal/trace"
)

// silentEnv is a task environment with no tools of its own, so every call the
// fuzzer makes lands on the communication tools or the fallback.
type silentEnv struct{}

func (silentEnv) Tools(string) []llm.Tool { return nil }
func (silentEnv) Call(string, string, json.RawMessage) (string, bool) {
	return "error: unknown tool", false
}
func (silentEnv) Done() bool { return false }

// The communication tools decode input the model wrote. Whatever it sends,
// the agent loop must hand back a result instead of panicking, and a send or
// broadcast it cannot decode must come back flagged as an error so the model
// sees its mistake rather than believing the message went out.
func FuzzExec(f *testing.F) {
	for _, tool := range []string{"send", "broadcast", "wait", "flag"} {
		for _, in := range []string{`{}`, `{"to":"agent-2","text":"hi"}`, `{"seconds":-5}`, `{"seconds":1e300}`, `null`, `{`, `[]`} {
			f.Add(tool, []byte(in))
		}
	}
	f.Fuzz(func(t *testing.T, tool string, input []byte) {
		ids := []string{"agent-1", "agent-2"}
		tr := trace.New()
		b := bus.New(bus.Config{Enabled: true}, ids, 1, tr)
		c := Config{ID: "agent-1", Comm: true, CanBroadcast: true, WaitTimeout: time.Millisecond}
		res, _, isErr := exec(context.Background(), c, b, silentEnv{}, llm.Block{Type: "tool_use", Name: tool, Input: input}, tr)
		if res == "" {
			t.Fatalf("%s returned an empty result", tool)
		}
		if (tool == "send" || tool == "broadcast") && !gjson.ValidBytes(input) && !isErr {
			t.Fatalf("%s accepted undecodable input %q: %s", tool, input, res)
		}
	})
}
