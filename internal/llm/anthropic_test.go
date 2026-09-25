package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goccy/go-json"
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
