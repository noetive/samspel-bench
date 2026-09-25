package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

// A fake Messages API: first call is throttled with retry-after, then it
// answers with a tool_use. Checks headers and cache breakpoints.
func TestClientRetriesAndCaching(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
		}
		var req apiRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if len(req.System) != 1 || req.System[0].CacheControl == nil {
			t.Errorf("system prompt should carry a cache breakpoint")
		}
		last := req.Messages[len(req.Messages)-1]
		if last.Content[len(last.Content)-1].CacheControl == nil {
			t.Errorf("last block should carry a cache breakpoint")
		}
		if req.Messages[0].Content[0].CacheControl != nil && len(req.Messages) > 1 {
			t.Errorf("only the last message should be marked")
		}
		if calls.Add(1) == 1 {
			w.Header().Set("retry-after", "0.05")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","id":"toolu_1","name":"submit","input":{"answer":"x"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3}}`))
	}))
	defer srv.Close()

	c := NewClient("k", srv.URL, NewLimiterSet(Limits{MaxInflight: 2, RPM: 600, ITPM: 100000, OTPM: 100000}, nil), 4)
	msgs := []Message{{Role: "user", Content: []Block{Text("hi")}}, {Role: "assistant", Content: []Block{Text("ok")}}, {Role: "user", Content: []Block{Text("go")}}}
	resp, err := c.Complete(context.Background(), Request{Model: "m", System: "sys", Messages: msgs, MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content[0].Name != "submit" || resp.Usage.CacheReadInputTokens != 3 {
		t.Fatalf("unexpected response %+v", resp)
	}
	if c.Throttled.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("throttled=%d calls=%d", c.Throttled.Load(), calls.Load())
	}
	if msgs[2].Content[0].CacheControl != nil {
		t.Fatalf("caller's messages were mutated")
	}
}

func TestClientDoesNotRetry400(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	}))
	defer srv.Close()
	c := NewClient("k", srv.URL, NewLimiterSet(Limits{}, nil), 4)
	_, err := c.Complete(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: []Block{Text("x")}}}, MaxTokens: 10})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestLimiterInflightCap(t *testing.T) {
	l := newLimiter(Limits{MaxInflight: 2})
	ctx := context.Background()
	r1, _ := l.Acquire(ctx, 1, 1)
	r2, _ := l.Acquire(ctx, 1, 1)
	got := make(chan struct{})
	go func() { r3, _ := l.Acquire(ctx, 1, 1); r3(0, 0); close(got) }()
	select {
	case <-got:
		t.Fatal("third acquire should block")
	case <-time.After(50 * time.Millisecond):
	}
	r1(1, 1)
	<-got
	r2(1, 1)
}

func TestBucketRate(t *testing.T) {
	b := newBucket(600) // 10 per second
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 605; i++ {
		if err := b.take(ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 300*time.Millisecond {
		t.Fatalf("600 burst + 5 more should wait ~0.5s, took %s", d)
	}
}

// A key that is not scoped to a workspace is refused unless every request
// names one, and a scoped key must not carry a workspace it was not given.
func TestClientSendsWorkspaceOnlyWhenSet(t *testing.T) {
	for _, ws := range []string{"", "wrkspc_test"} {
		var got atomic.Value
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got.Store(r.Header.Values("anthropic-workspace-id"))
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
		}))
		c := NewClient("k", srv.URL, NewLimiterSet(Limits{MaxInflight: 1, RPM: 600, ITPM: 100000, OTPM: 100000}, nil), 1)
		c.WorkspaceID = ws
		if _, err := c.Complete(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: []Block{Text("hi")}}}, MaxTokens: 10}); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		vals := got.Load().([]string)
		if ws == "" && len(vals) != 0 {
			t.Errorf("header sent without a workspace: %v", vals)
		}
		if ws != "" && (len(vals) != 1 || vals[0] != ws) {
			t.Errorf("header = %v, want [%s]", vals, ws)
		}
	}
}

// Models that think return thinking blocks the API expects back unchanged on
// the next turn, signature and all; a block missing a field is a 400 that
// fails the whole run.
func TestResponseBlocksReplayUnchanged(t *testing.T) {
	thinking := `{"type":"thinking","thinking":"","signature":"c2lnbmF0dXJl"}`
	redacted := `{"type":"redacted_thinking","data":"b3BhcXVl"}`
	body := []byte(`{"content":[` + thinking + `,` + redacted + `,{"type":"tool_use","id":"t1","name":"send","input":{"to":"agent-2","text":"hi"}}],"stop_reason":"tool_use","usage":{}}`)
	resp, err := decodeResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []Message{
		{Role: "user", Content: []Block{Text("go")}},
		{Role: "assistant", Content: resp.Content},
		{Role: "user", Content: []Block{ToolResult("t1", "sent", false)}},
	}
	b, err := json.Marshal(buildRequest(Request{Model: "m", Messages: msgs, MaxTokens: 10}))
	if err != nil {
		t.Fatal(err)
	}
	sent := gjson.GetBytes(b, "messages.1.content")
	if got := sent.Get("0").Raw; got != thinking {
		t.Errorf("thinking block re-sent as %s, want %s", got, thinking)
	}
	if got := sent.Get("1").Raw; got != redacted {
		t.Errorf("redacted block re-sent as %s, want %s", got, redacted)
	}
	if got := sent.Get("2.input.text").String(); got != "hi" {
		t.Errorf("tool_use input lost: %s", sent.Get("2").Raw)
	}
	if !gjson.GetBytes(b, "messages.2.content.0.cache_control").Exists() {
		t.Error("the cache breakpoint on the last block is gone")
	}
}

// A run cut off while its request was retrying a server or network failure
// must be marked transient, so it is rerun instead of scored as the agents
// running out of time; a request the API refuses outright must not be, or a
// bad request would be retried and paid for again.
func TestCompleteMarksOnlyRetryableFailuresTransient(t *testing.T) {
	for _, tc := range []struct {
		status    int
		transient bool
	}{{503, true}, {529, true}, {429, true}, {400, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"x","message":"no"}}`))
		}))
		c := NewClient("k", srv.URL, NewLimiterSet(Limits{MaxInflight: 1, RPM: 6000, ITPM: 1e6, OTPM: 1e6}, nil), 1)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, err := c.Complete(ctx, Request{Model: "m", Messages: []Message{{Role: "user", Content: []Block{Text("hi")}}}, MaxTokens: 10})
		cancel()
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: no error", tc.status)
		}
		if got := errors.Is(err, ErrTransient); got != tc.transient {
			t.Errorf("status %d: transient = %v, want %v (%v)", tc.status, got, tc.transient, err)
		}
	}
}
