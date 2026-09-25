package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

const apiVersion = "2023-06-01"

// Client calls the Anthropic Messages API. It is safe for concurrent use and
// is shared by every agent in every parallel run.
type Client struct {
	APIKey  string
	BaseURL string
	// WorkspaceID selects the workspace for a key that is not scoped to one;
	// the API refuses such a key without it. Empty sends no header.
	WorkspaceID string
	HTTP        *http.Client
	Limiters    *LimiterSet
	MaxRetries  int

	Requests   atomic.Int64
	Throttled  atomic.Int64 // 429
	Overloaded atomic.Int64 // 529
	ServerErrs atomic.Int64 // other 5xx and network errors
	Failed     atomic.Int64
}

// NewClient builds a client with a connection pool sized for maxConns
// concurrent requests.
func NewClient(apiKey, baseURL string, lim *LimiterSet, maxConns int) *Client {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if maxConns < 16 {
		maxConns = 16
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        maxConns * 2,
		MaxIdleConnsPerHost: maxConns * 2,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	return &Client{
		APIKey:     apiKey,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTP:       &http.Client{Transport: tr, Timeout: 10 * time.Minute},
		Limiters:   lim,
		MaxRetries: 8,
	}
}

// APIError is a non-200 response.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300]
	}
	return fmt.Sprintf("anthropic API status %d: %s", e.Status, b)
}

type apiRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	System      []Block   `json:"system,omitempty"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
}

// buildRequest adds two prompt-cache breakpoints: after the system prompt
// (which also covers the tool definitions) and on the last block of the
// conversation, so each turn reads the previous turn's prefix from cache.
// The caller's messages are not modified.
func buildRequest(req Request) apiRequest {
	out := apiRequest{Model: req.Model, MaxTokens: req.MaxTokens, Tools: req.Tools, Temperature: req.Temperature}
	if req.System != "" {
		out.System = []Block{{Type: "text", Text: req.System, CacheControl: &CacheControl{Type: "ephemeral"}}}
	}
	out.Messages = make([]Message, len(req.Messages))
	copy(out.Messages, req.Messages)
	if n := len(out.Messages); n > 0 && len(out.Messages[n-1].Content) > 0 {
		last := out.Messages[n-1]
		c := make([]Block, len(last.Content))
		copy(c, last.Content)
		c[len(c)-1].CacheControl = &CacheControl{Type: "ephemeral"}
		out.Messages[n-1] = Message{Role: last.Role, Content: c}
	}
	return out
}

// ErrTransient marks a request that failed only through conditions worth
// retrying later: throttling, overload, server errors or a lost connection,
// including a run deadline that expired while waiting to retry one of them.
// A run that ends this way says nothing about the agents.
var ErrTransient = errors.New("transient API failure")

// Complete sends one request, waiting on the shared limiter and retrying
// 429, 529, 5xx and network errors with jittered backoff. A 429 pauses every
// caller of the same model for the server's retry-after.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	payload, err := json.Marshal(buildRequest(req))
	if err != nil {
		return nil, err
	}
	estIn := len(payload) / 4
	lim := c.Limiters.For(req.Model)
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		release, err := lim.Acquire(ctx, estIn, req.MaxTokens)
		if err != nil {
			return nil, err
		}
		c.Requests.Add(1)
		resp, hdr, err := c.post(ctx, payload)
		if err == nil {
			// Cache reads are left out of the input-token settlement.
			release(resp.Usage.InputTokens+resp.Usage.CacheCreationInputTokens, resp.Usage.OutputTokens)
			return resp, nil
		}
		release(0, 0)
		if ctx.Err() != nil {
			return nil, interrupted(ctx, lastErr)
		}
		lastErr = err
		wait := backoff(attempt)
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			switch {
			case apiErr.Status == 429:
				c.Throttled.Add(1)
				if ra := retryAfter(hdr); ra > 0 {
					wait = ra
				}
				lim.Cooldown(wait)
			case apiErr.Status == 529:
				c.Overloaded.Add(1)
			case apiErr.Status >= 500 || apiErr.Status == 408 || apiErr.Status == 409:
				c.ServerErrs.Add(1)
			default:
				c.Failed.Add(1)
				return nil, err
			}
		} else {
			c.ServerErrs.Add(1)
		}
		if sleep(ctx, wait) != nil {
			return nil, interrupted(ctx, lastErr)
		}
	}
	c.Failed.Add(1)
	return nil, fmt.Errorf("%w: giving up after %d attempts: %w", ErrTransient, c.MaxRetries+1, lastErr)
}

// interrupted reports a request cut off by its context. After a transient
// failure the cut is blamed on that failure, so a network outage that eats a
// run's time budget is not scored as the agents running out of time.
func interrupted(ctx context.Context, lastErr error) error {
	if lastErr == nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %w while retrying: %w", ErrTransient, ctx.Err(), lastErr)
}

func (c *Client) post(ctx context.Context, payload []byte) (*Response, http.Header, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	hreq.Header.Set("x-api-key", c.APIKey)
	hreq.Header.Set("anthropic-version", apiVersion)
	hreq.Header.Set("content-type", "application/json")
	if c.WorkspaceID != "" {
		hreq.Header.Set("anthropic-workspace-id", c.WorkspaceID)
	}
	hresp, err := c.HTTP.Do(hreq)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = hresp.Body.Close() }() // fully read below; nothing is lost on close
	body, err := io.ReadAll(hresp.Body)
	if err != nil {
		return nil, hresp.Header, err
	}
	if hresp.StatusCode != http.StatusOK {
		return nil, hresp.Header, &APIError{Status: hresp.StatusCode, Body: string(body)}
	}
	out, err := decodeResponse(body)
	if err != nil {
		return nil, hresp.Header, fmt.Errorf("decode response: %w", err)
	}
	return out, hresp.Header, nil
}

// decodeResponse reads the fields the harness uses from a response body. The
// body carries model-written tool inputs, which are re-sent on every later
// turn, so the whole body must be valid JSON before any of it is kept.
func decodeResponse(body []byte) (*Response, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("body is not valid JSON")
	}
	r := gjson.ParseBytes(body)
	u := r.Get("usage")
	out := &Response{
		StopReason: r.Get("stop_reason").String(),
		Usage: Usage{
			InputTokens:              int(u.Get("input_tokens").Int()),
			OutputTokens:             int(u.Get("output_tokens").Int()),
			CacheCreationInputTokens: int(u.Get("cache_creation_input_tokens").Int()),
			CacheReadInputTokens:     int(u.Get("cache_read_input_tokens").Int()),
		},
	}
	for _, b := range r.Get("content").Array() {
		blk := Block{Type: b.Get("type").String(), Text: b.Get("text").String(), ID: b.Get("id").String(), Name: b.Get("name").String()}
		blk.Raw = json.RawMessage(b.Raw)
		if in := b.Get("input"); in.Exists() {
			blk.Input = json.RawMessage(in.Raw)
		}
		out.Content = append(out.Content, blk)
	}
	return out, nil
}

func retryAfter(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
	if s := h.Get("retry-after"); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	}
	return 0
}

func backoff(attempt int) time.Duration {
	d := time.Second << uint(attempt)
	if d > 60*time.Second || d <= 0 {
		d = 60 * time.Second
	}
	return time.Duration(float64(d) * (0.5 + rand.Float64()/2))
}
