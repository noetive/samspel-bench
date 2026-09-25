package llm

import (
	"context"
	"sync"
	"time"
)

// Limits caps traffic to one model. Zero means unlimited.
type Limits struct {
	MaxInflight int `json:"max_inflight"`
	RPM         int `json:"rpm"`
	ITPM        int `json:"itpm"` // uncached input tokens per minute
	OTPM        int `json:"otpm"` // output tokens per minute
}

// LimiterSet hands out one shared limiter per model, so every run and every
// agent in the process draws from the same budget.
type LimiterSet struct {
	mu  sync.Mutex
	def Limits
	per map[string]Limits
	m   map[string]*Limiter
}

func NewLimiterSet(def Limits, per map[string]Limits) *LimiterSet {
	return &LimiterSet{def: def, per: per, m: map[string]*Limiter{}}
}

// For returns the limiter for a model, creating it on first use.
func (s *LimiterSet) For(model string) *Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.m[model]; ok {
		return l
	}
	lim := s.def
	if p, ok := s.per[model]; ok {
		lim = p
	}
	l := newLimiter(lim)
	s.m[model] = l
	return l
}

// Limiter combines an in-flight cap, three token buckets and a shared
// cooldown that every caller honors after a 429.
type Limiter struct {
	sem             chan struct{}
	rpm, itpm, otpm *bucket
	mu              sync.Mutex
	cooldownUntil   time.Time
}

func newLimiter(l Limits) *Limiter {
	lim := &Limiter{rpm: newBucket(l.RPM), itpm: newBucket(l.ITPM), otpm: newBucket(l.OTPM)}
	if l.MaxInflight > 0 {
		lim.sem = make(chan struct{}, l.MaxInflight)
	}
	return lim
}

// Cooldown pauses every caller of this limiter for d.
func (l *Limiter) Cooldown(d time.Duration) {
	l.mu.Lock()
	if until := time.Now().Add(d); until.After(l.cooldownUntil) {
		l.cooldownUntil = until
	}
	l.mu.Unlock()
}

func (l *Limiter) waitCooldown(ctx context.Context) error {
	for {
		l.mu.Lock()
		d := time.Until(l.cooldownUntil)
		l.mu.Unlock()
		if d <= 0 {
			return ctx.Err()
		}
		if err := sleep(ctx, d); err != nil {
			return err
		}
	}
}

// Acquire reserves capacity for one request. The returned release settles the
// reservation against actual usage. It is safe to call more than once; only
// the first call counts.
func (l *Limiter) Acquire(ctx context.Context, inTok, outTok int) (func(actualIn, actualOut int), error) {
	if err := l.waitCooldown(ctx); err != nil {
		return nil, err
	}
	if err := l.rpm.take(ctx, 1); err != nil {
		return nil, err
	}
	if err := l.itpm.take(ctx, float64(inTok)); err != nil {
		return nil, err
	}
	if err := l.otpm.take(ctx, float64(outTok)); err != nil {
		l.itpm.give(float64(inTok))
		return nil, err
	}
	if l.sem != nil {
		select {
		case l.sem <- struct{}{}:
		case <-ctx.Done():
			l.itpm.give(float64(inTok))
			l.otpm.give(float64(outTok))
			return nil, ctx.Err()
		}
	}
	var once sync.Once
	return func(actualIn, actualOut int) {
		once.Do(func() {
			l.itpm.give(float64(inTok - actualIn))
			l.otpm.give(float64(outTok - actualOut))
			if l.sem != nil {
				<-l.sem
			}
		})
	}, nil
}

// bucket is a token bucket refilled continuously at capacity per minute.
// A negative give records debt from an under-estimated reservation.
type bucket struct {
	mu     sync.Mutex
	cap    float64
	tokens float64
	rate   float64 // per second
	last   time.Time
}

func newBucket(perMinute int) *bucket {
	if perMinute <= 0 {
		return nil
	}
	c := float64(perMinute)
	return &bucket{cap: c, tokens: c, rate: c / 60, last: time.Now()}
}

func (b *bucket) refill() {
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.cap {
		b.tokens = b.cap
	}
	b.last = now
}

func (b *bucket) take(ctx context.Context, n float64) error {
	if b == nil {
		return nil
	}
	if n > b.cap {
		n = b.cap
	}
	for {
		b.mu.Lock()
		b.refill()
		if b.tokens >= n {
			b.tokens -= n
			b.mu.Unlock()
			return nil
		}
		need := time.Duration((n - b.tokens) / b.rate * float64(time.Second))
		b.mu.Unlock()
		if need > time.Second {
			need = time.Second
		}
		if need < time.Millisecond {
			need = time.Millisecond
		}
		if err := sleep(ctx, need); err != nil {
			return err
		}
	}
}

func (b *bucket) give(n float64) {
	if b == nil || n == 0 {
		return
	}
	b.mu.Lock()
	b.refill()
	b.tokens += n
	if b.tokens > b.cap {
		b.tokens = b.cap
	}
	b.mu.Unlock()
}
