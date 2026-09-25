package llm

import (
	"reflect"
	"testing"

	"github.com/goccy/go-json"
)

// A response body comes from the network and carries model-written tool
// inputs. Decoding must never panic, and whatever decodes must survive a
// round trip unchanged, because the conversation is re-sent from the decoded
// form on every later turn.
func FuzzResponseDecode(f *testing.F) {
	f.Add([]byte(`{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	f.Add([]byte(`{"content":[{"type":"tool_use","id":"t1","name":"send","input":{"to":"agent-2","text":"x"}}],"stop_reason":"tool_use"}`))
	f.Add([]byte(`{"content":[{"type":"tool_use","input":[1,{"a":null}]}]}`))
	f.Add([]byte(`{"content":null,"usage":{"input_tokens":-1}}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		first, err := decodeResponse(body)
		if err != nil {
			return
		}
		again, err := json.Marshal(first)
		if err != nil {
			t.Fatalf("decoded response does not re-encode: %v", err)
		}
		second, err := decodeResponse(again)
		if err != nil {
			t.Fatalf("re-encoded response does not decode: %v\n%s", err, again)
		}
		if !reflect.DeepEqual(normalize(t, *first), normalize(t, *second)) {
			t.Fatalf("round trip changed the response:\n%s", again)
		}
	})
}

// normalize compares tool inputs by meaning, not by byte layout: re-encoding
// may compact whitespace inside a raw input without changing it.
func normalize(t *testing.T, r Response) Response {
	t.Helper()
	for i, b := range r.Content {
		if len(b.Input) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(b.Input, &v); err != nil {
			t.Fatalf("tool input is not JSON after decode: %v", err)
		}
		c, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		r.Content[i].Input = c
	}
	return r
}
