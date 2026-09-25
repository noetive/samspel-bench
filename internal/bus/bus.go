// Package bus is the only channel between agents in a run. It enforces the
// topology and budgets, injects channel faults, detects quiescence, and
// records every send and delivery in the trace.
package bus

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/noetive/samspel-bench/internal/trace"
)

// Config sets the channel conditions for one run.
type Config struct {
	Enabled          bool
	Allowed          func(from, to string) bool // nil allows every pair
	CanBroadcast     func(from string) bool     // nil allows everyone
	Drop             float64                    // probability a delivery is lost
	Duplicate        float64                    // probability a delivery arrives twice
	MaxDelay         time.Duration              // uniform random delay per delivery; reorders messages
	Ablate           bool                       // replace content with length-matched noise
	BudgetTokens     int                        // per-agent total of sent message tokens; 0 is unlimited
	MaxMessageTokens int                        // per-message cap; 0 is unlimited
}

// Message is one delivered message.
type Message struct {
	ID       int64
	From, To string
	Text     string
}

// Bus is safe for concurrent use.
type Bus struct {
	cfg     Config
	tr      *trace.Trace
	mu      sync.Mutex
	rng     *rand.Rand
	agents  map[string]bool
	order   []string
	inbox   map[string][]Message
	notify  map[string]chan struct{}
	active  map[string]bool
	waiting map[string]bool
	quiet   map[string]bool
	pending int
	sent    map[string]int
	nextID  int64
}

// New creates a bus for the given participants.
func New(cfg Config, agents []string, seed uint64, tr *trace.Trace) *Bus {
	b := &Bus{
		cfg: cfg, tr: tr, rng: rand.New(rand.NewPCG(seed, 0xb5ad4eceda1ce2a9)),
		agents: map[string]bool{}, inbox: map[string][]Message{}, notify: map[string]chan struct{}{},
		active: map[string]bool{}, waiting: map[string]bool{}, quiet: map[string]bool{}, sent: map[string]int{},
	}
	for _, a := range agents {
		b.agents[a] = true
		b.active[a] = true
		b.notify[a] = make(chan struct{}, 1)
		b.order = append(b.order, a)
	}
	sort.Strings(b.order)
	return b
}

// Tokens approximates a token count from characters.
func Tokens(s string) int { return (len(s) + 3) / 4 }

// Peers lists every participant except id.
func (b *Bus) Peers(id string) []string {
	var out []string
	for _, a := range b.order {
		if a != id {
			out = append(out, a)
		}
	}
	return out
}

// SentTokens is the message-token total an agent has spent.
func (b *Bus) SentTokens(id string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sent[id]
}

func (b *Bus) allowed(from, to string) bool {
	return b.cfg.Allowed == nil || b.cfg.Allowed(from, to)
}

// charge applies the size and budget rules. Lock held.
func (b *Bus) charge(from, text string) string {
	tok := Tokens(text)
	if strings.TrimSpace(text) == "" {
		return "rejected: empty message"
	}
	if b.cfg.MaxMessageTokens > 0 && tok > b.cfg.MaxMessageTokens {
		return fmt.Sprintf("rejected: message is about %d tokens and the limit is %d per message. Shorten it or split it into several messages.", tok, b.cfg.MaxMessageTokens)
	}
	if b.cfg.BudgetTokens > 0 && b.sent[from]+tok > b.cfg.BudgetTokens {
		return fmt.Sprintf("rejected: this would exceed your message budget of %d tokens (%d used, this message is about %d).", b.cfg.BudgetTokens, b.sent[from], tok)
	}
	b.sent[from] += tok
	return ""
}

// Send delivers text from one agent to another. It returns the tool result
// text and whether the send was accepted.
func (b *Bus) Send(from, to, text string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.cfg.Enabled {
		return "communication is disabled in this run", false
	}
	if !b.agents[to] || to == from {
		return fmt.Sprintf("unknown recipient %q; valid recipients: %s", to, strings.Join(b.Peers(from), ", ")), false
	}
	if !b.allowed(from, to) {
		return fmt.Sprintf("you are not allowed to send to %s in this topology", to), false
	}
	if msg := b.charge(from, text); msg != "" {
		b.tr.Add(trace.Event{Kind: "reject", Agent: from, To: to, Text: text, Result: msg})
		return msg, false
	}
	b.nextID++
	id := b.nextID
	b.tr.Add(trace.Event{Kind: "send", Agent: from, To: to, MsgID: id, Text: text})
	b.route(Message{ID: id, From: from, To: to, Text: text})
	return fmt.Sprintf("sent to %s (message %d)", to, id), true
}

// Broadcast delivers text to every participant the sender may reach. The
// sender's budget is charged once.
func (b *Bus) Broadcast(from, text string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.cfg.Enabled {
		return "communication is disabled in this run", false
	}
	if b.cfg.CanBroadcast != nil && !b.cfg.CanBroadcast(from) {
		return "broadcast is not allowed in this topology; use send", false
	}
	var to []string
	for _, a := range b.order {
		if a != from && b.allowed(from, a) {
			to = append(to, a)
		}
	}
	if len(to) == 0 {
		return "no reachable recipients", false
	}
	if msg := b.charge(from, text); msg != "" {
		b.tr.Add(trace.Event{Kind: "reject", Agent: from, To: "*", Text: text, Result: msg})
		return msg, false
	}
	b.nextID++
	id := b.nextID
	b.tr.Add(trace.Event{Kind: "send", Agent: from, To: "*", MsgID: id, Text: text})
	for _, a := range to {
		b.route(Message{ID: id, From: from, To: a, Text: text})
	}
	return fmt.Sprintf("broadcast to %d agents (message %d)", len(to), id), true
}

// route applies channel faults to one delivery. Lock held.
func (b *Bus) route(m Message) {
	if b.cfg.Drop > 0 && b.rng.Float64() < b.cfg.Drop {
		b.tr.Add(trace.Event{Kind: "drop", Agent: m.From, To: m.To, MsgID: m.ID})
		return
	}
	copies := 1
	if b.cfg.Duplicate > 0 && b.rng.Float64() < b.cfg.Duplicate {
		copies = 2
		b.tr.Add(trace.Event{Kind: "duplicate", Agent: m.From, To: m.To, MsgID: m.ID})
	}
	if b.cfg.Ablate {
		m.Text = noise(b.rng, len(m.Text))
	}
	for i := 0; i < copies; i++ {
		mm := m
		if b.cfg.MaxDelay > 0 {
			d := time.Duration(b.rng.Int64N(int64(b.cfg.MaxDelay)))
			b.pending++
			time.AfterFunc(d, func() {
				b.mu.Lock()
				b.pending--
				b.put(mm)
				b.checkQuiet()
				b.mu.Unlock()
			})
			continue
		}
		b.put(mm)
	}
}

// put places a message in an inbox. Lock held.
func (b *Bus) put(m Message) {
	b.inbox[m.To] = append(b.inbox[m.To], m)
	b.tr.Add(trace.Event{Kind: "deliver", Agent: m.From, To: m.To, MsgID: m.ID})
	b.signal(m.To)
}

func (b *Bus) signal(a string) {
	if ch, ok := b.notify[a]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Drain returns and clears an agent's inbox.
func (b *Bus) Drain(a string) []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.drainLocked(a)
}

func (b *Bus) drainLocked(a string) []Message {
	m := b.inbox[a]
	b.inbox[a] = nil
	return m
}

// Wait blocks until the agent has mail, the timeout passes, or every active
// participant is waiting with nothing in flight. The second result reports
// that quiescent state: nothing will arrive unless someone acts.
func (b *Bus) Wait(ctx context.Context, a string, timeout time.Duration) ([]Message, bool) {
	b.mu.Lock()
	if msgs := b.drainLocked(a); len(msgs) > 0 {
		b.mu.Unlock()
		return msgs, false
	}
	ch := b.notify[a]
	select { // clear a stale signal
	case <-ch:
	default:
	}
	b.waiting[a] = true
	b.checkQuiet()
	b.mu.Unlock()

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-ctx.Done():
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.waiting, a)
	q := b.quiet[a]
	delete(b.quiet, a)
	return b.drainLocked(a), q
}

// Leave removes an agent from the active set (finished, crashed or done).
func (b *Bus) Leave(a string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.active, a)
	delete(b.waiting, a)
	b.checkQuiet()
}

// checkQuiet wakes every waiter if all active agents are waiting with empty
// inboxes and no delayed delivery is pending. Lock held.
func (b *Bus) checkQuiet() {
	if b.pending > 0 || len(b.waiting) == 0 {
		return
	}
	for a := range b.active {
		if !b.waiting[a] || len(b.inbox[a]) > 0 {
			return
		}
	}
	for a := range b.waiting {
		b.quiet[a] = true
		b.signal(a)
	}
}

var noiseWords = strings.Fields("lorem ipsum dolor sit amet orbit cobalt vector lantern meadow quartz ripple tundra velvet walnut zephyr harbor signal pebble canyon")

func noise(r *rand.Rand, n int) string {
	var sb strings.Builder
	for sb.Len() < n {
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(noiseWords[r.IntN(len(noiseWords))])
	}
	return sb.String()
}
