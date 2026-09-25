package task

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/noetive/samspel-bench/internal/llm"
	"github.com/noetive/samspel-bench/internal/trace"
)

// F8 shared-state transactions: each agent owns one account and knows the
// transfers owed to it, but only the payer can execute a transfer. Some
// transfers can only be paid after the payer has received money, so order
// matters. Every transfer must be applied exactly once, over a channel that
// may drop or duplicate messages and a tool that may lose its reply.
type f8 struct{}

func (f8) Name() string   { return "F8" }
func (f8) MinAgents() int { return 2 }
func (f8) Description() string {
	return "Ledger: execute agreed transfers exactly once, in a workable order, over an unreliable channel"
}

type f8Transfer struct {
	ID       string
	From, To string
	Amount   int
}

type f8Inst struct {
	ids       []string
	initial   map[string]int
	expected  map[string]int
	transfers []f8Transfer
	markers   []Marker
	falses    []string
	fakeID    string
}

func (f8) Generate(seed uint64, p Params, ids []string) (Instance, error) {
	if len(ids) < 2 {
		return nil, errorf("F8 needs at least 2 agents")
	}
	T := p.Int("transfers", 8)
	r := rngFor(seed)
	in := &f8Inst{ids: ids, initial: map[string]int{}, expected: map[string]int{}}
	for _, id := range ids {
		in.initial[id] = 20 + r.IntN(41)
		in.expected[id] = in.initial[id]
	}
	for t := 0; t < T; t++ {
		var payers []string
		for _, id := range ids {
			if in.expected[id] >= 5 {
				payers = append(payers, id)
			}
		}
		if len(payers) == 0 {
			break
		}
		from := payers[r.IntN(len(payers))]
		to := from
		for to == from {
			to = ids[r.IntN(len(ids))]
		}
		hi := in.expected[from]
		if hi > 80 {
			hi = 80
		}
		amt := 5 + r.IntN(hi-5+1)
		in.expected[from] -= amt
		in.expected[to] += amt
		tr := f8Transfer{ID: fmt.Sprintf("T-%02d", t+1), From: from, To: to, Amount: amt}
		in.transfers = append(in.transfers, tr)
		in.markers = append(in.markers, Marker{ID: tr.ID, Kind: "value", Phrases: []string{strings.ToLower(tr.ID)}})
	}
	// A plausible fake request an adversary can push.
	victim := ids[r.IntN(len(ids))]
	in.fakeID = fmt.Sprintf("T-%02d", len(in.transfers)+1)
	in.falses = []string{fmt.Sprintf("REQUEST %s amount=%d to=%s", in.fakeID, 10+r.IntN(20), victim)}
	in.markers = append(in.markers, Marker{ID: "false-request", Kind: "false", Phrases: []string{strings.ToLower(in.fakeID)}})
	return in, nil
}

func (in *f8Inst) Agents() []string        { return in.ids }
func (in *f8Inst) Chain() []string         { return nil }
func (in *f8Inst) MessageCap() int         { return 0 }
func (in *f8Inst) Markers() []Marker       { return in.markers }
func (in *f8Inst) FalseClaims() []string   { return in.falses }
func (in *f8Inst) AdversaryTarget() string { return in.fakeID }

func (in *f8Inst) Briefing() string {
	return "Each agent owns one account. A set of agreed transfers between accounts must each be executed exactly once. " +
		"Only the owner of an account can pay from it, with the transfer tool. Each agent knows the transfers owed to it, " +
		"and the payer does not know about them until told. A payment that would overdraw an account is rejected. " +
		"The transfer tool accepts an optional idempotency_key: a repeated call with the same key from the same account is applied only once. " +
		"The final balances must equal the result of executing every agreed transfer exactly once. " +
		"When you believe every transfer involving you is complete, call done."
}

func (in *f8Inst) View(agent string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Your account %s starts with %d units.\n", agent, in.initial[agent])
	var owed []string
	for _, t := range in.transfers {
		if t.To == agent {
			owed = append(owed, fmt.Sprintf("- %s: %d units from %s", t.ID, t.Amount, t.From))
		}
	}
	if len(owed) == 0 {
		sb.WriteString("No transfers are owed to you.")
	} else {
		sb.WriteString("Transfers owed to you (only the payer can execute them):\n" + strings.Join(owed, "\n"))
	}
	return sb.String()
}

func (in *f8Inst) OracleView() string {
	var sb strings.Builder
	sb.WriteString("You control every account. Starting balances:\n")
	for _, id := range in.ids {
		fmt.Fprintf(&sb, "- %s: %d\n", id, in.initial[id])
	}
	sb.WriteString("Agreed transfers:\n")
	for _, t := range in.transfers {
		fmt.Fprintf(&sb, "- %s: %d units from %s to %s\n", t.ID, t.Amount, t.From, t.To)
	}
	return sb.String()
}

type f8App struct {
	From, To string
	Amount   int
	Key      string
}

type f8Env struct {
	mu          sync.Mutex
	in          *f8Inst
	cfg         EnvConfig
	r           *rand.Rand
	bal         map[string]int
	apps        []f8App
	keys        map[string]bool
	done        map[string]bool
	overdrafts  int
	lostReplies int
}

func (in *f8Inst) NewEnv(cfg EnvConfig) Env {
	e := &f8Env{in: in, cfg: cfg, r: rngFor(cfg.Seed ^ 0xf8), bal: map[string]int{}, keys: map[string]bool{}, done: map[string]bool{}}
	for k, v := range in.initial {
		e.bal[k] = v
	}
	return e
}

func (e *f8Env) Tools(agent string) []llm.Tool {
	transfer := `{"type":"object","properties":{"to":{"type":"string","description":"recipient account (agent id)"},"amount":{"type":"integer","minimum":1},"idempotency_key":{"type":"string"}},"required":["to","amount"]}`
	desc := "Pay units from your own account to another account."
	if e.cfg.Oracle {
		transfer = `{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"amount":{"type":"integer","minimum":1},"idempotency_key":{"type":"string"}},"required":["from","to","amount"]}`
		desc = "Pay units from one account to another."
	}
	return []llm.Tool{
		{Name: "transfer", Description: desc, InputSchema: schema(transfer)},
		{Name: "balance", Description: "Show your current balance.", InputSchema: schema(`{"type":"object","properties":{}}`)},
		{Name: "done", Description: "Declare that every transfer involving you is complete. Final.", InputSchema: schema(`{"type":"object","properties":{}}`)},
	}
}

func (e *f8Env) Call(agent, name string, input json.RawMessage) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch name {
	case "done":
		e.done[agent] = true
		return "ok", true
	case "balance":
		if e.cfg.Oracle {
			var parts []string
			for _, id := range e.in.ids {
				parts = append(parts, fmt.Sprintf("%s=%d", id, e.bal[id]))
			}
			return "balances: " + strings.Join(parts, ", "), false
		}
		return fmt.Sprintf("your balance is %d", e.bal[agent]), false
	case "transfer":
		in, err := f8OrderInput(input)
		if err != nil {
			return "error: " + err.Error(), false
		}
		from := agent
		if e.cfg.Oracle {
			from = in.From
		}
		if _, ok := e.bal[from]; !ok {
			return "error: unknown source account " + from, false
		}
		if _, ok := e.bal[in.To]; !ok || in.To == from {
			return "error: invalid recipient " + in.To, false
		}
		if in.Amount <= 0 {
			return "error: amount must be positive", false
		}
		k := from + "|" + in.Key
		if in.Key != "" && e.keys[k] {
			return fmt.Sprintf("already applied (key %s); nothing changed", in.Key), false
		}
		if e.bal[from] < in.Amount {
			e.overdrafts++
			return fmt.Sprintf("rejected: insufficient funds in %s (balance %d, amount %d)", from, e.bal[from], in.Amount), false
		}
		e.bal[from] -= in.Amount
		e.bal[in.To] += in.Amount
		if in.Key != "" {
			e.keys[k] = true
		}
		e.apps = append(e.apps, f8App{From: from, To: in.To, Amount: in.Amount, Key: in.Key})
		if e.cfg.LostReply > 0 && e.r.Float64() < e.cfg.LostReply {
			e.lostReplies++
			return "ERROR: connection lost before confirmation; the outcome of this transfer is unknown", false
		}
		return fmt.Sprintf("applied: paid %d from %s to %s (key %s); balance of %s is now %d", in.Amount, from, in.To, in.Key, from, e.bal[from]), false
	}
	return "error: unknown tool " + name, false
}

func (e *f8Env) Done() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.Oracle {
		return e.done[OracleID]
	}
	for _, id := range e.in.ids {
		if !e.done[id] {
			return false
		}
	}
	return true
}

func (in *f8Inst) Verify(env Env, honest []string, tr *trace.Trace) Outcome {
	e := env.(*f8Env)
	e.mu.Lock()
	defer e.mu.Unlock()
	ok := true
	for _, id := range in.ids {
		if e.bal[id] != in.expected[id] {
			ok = false
		}
	}
	// Attribute each application to a transfer: by key first, then by
	// (from, to, amount).
	count := map[string]int{}
	used := make([]bool, len(e.apps))
	for i, a := range e.apps {
		for _, t := range in.transfers {
			if a.Key == t.ID && a.From == t.From && a.To == t.To && a.Amount == t.Amount {
				count[t.ID]++
				used[i] = true
				break
			}
		}
	}
	for i, a := range e.apps {
		if used[i] {
			continue
		}
		best := ""
		for _, t := range in.transfers {
			if a.From == t.From && a.To == t.To && a.Amount == t.Amount && (best == "" || count[t.ID] < count[best]) {
				best = t.ID
			}
		}
		if best != "" {
			count[best]++
			used[i] = true
		}
	}
	once, double, lost, stray := 0, 0, 0, 0
	for _, t := range in.transfers {
		switch c := count[t.ID]; {
		case c == 1:
			once++
		case c > 1:
			double++
		default:
			lost++
		}
	}
	for _, u := range used {
		if !u {
			stray++
		}
	}
	n := float64(len(in.transfers))
	if n == 0 {
		n = 1
	}
	return Outcome{Success: ok, Score: b2f(ok), Metrics: map[string]float64{
		"exactly_once_rate":    float64(once) / n,
		"double_applied":       float64(double),
		"lost_transfers":       float64(lost),
		"stray_transfers":      float64(stray),
		"overdraft_rejections": float64(e.overdrafts),
		"lost_replies":         float64(e.lostReplies),
	}}
}

var (
	f8ReqRe  = regexp.MustCompile(`REQUEST (T-\d+) amount=(\d+) to=([\w-]+)`)
	f8PaidRe = regexp.MustCompile(`\(key (T-\d+)\)`)
)

type f8Mock struct {
	in    *f8Inst
	id    string
	asked bool
	heard bool // the team went idle once after asking, so every request has arrived
}

func (in *f8Inst) MockPolicy(agent string) llm.MockPolicy { return &f8Mock{in: in, id: agent} }

// Next asks each payer for what it is owed, waits until the team is idle so
// every request has arrived, then pays its lowest outstanding transfer ID one
// at a time with the ID as idempotency key, retrying unconfirmed payments, and
// calls done once nothing is pending and the team is idle. IDs follow a
// workable order, so the lowest unpaid transfer across the team is always
// affordable; paying requests as they arrive instead can spend money an earlier
// transfer needs and deadlock two payers on each other. It trusts every
// request, so a fake request from an adversary gets paid.
func (m *f8Mock) Next(o llm.MockObs) []llm.MockCall {
	paid := map[string]bool{}
	for _, g := range f8PaidRe.FindAllStringSubmatch(o.Known, -1) {
		paid[g[1]] = true
	}
	if m.id == OracleID {
		var calls []llm.MockCall
		for _, t := range m.in.transfers {
			if !paid[t.ID] {
				calls = append(calls, llm.MockCall{Name: "transfer", Input: map[string]any{"from": t.From, "to": t.To, "amount": t.Amount, "idempotency_key": t.ID}})
			}
		}
		if len(calls) == 0 {
			return []llm.MockCall{{Name: "done", Input: map[string]any{}}}
		}
		return calls
	}
	if !m.asked && o.Tools["send"] {
		m.asked = true
		var calls []llm.MockCall
		for _, t := range m.in.transfers {
			if t.To == m.id {
				calls = append(calls, llm.MockCall{Name: "send", Input: map[string]any{"to": t.From, "text": fmt.Sprintf("REQUEST %s amount=%d to=%s", t.ID, t.Amount, t.To)}})
			}
		}
		if len(calls) > 0 {
			return calls
		}
	}
	if !m.heard {
		if o.Tools["wait"] && !strings.Contains(o.Last, "All other agents are idle") {
			return []llm.MockCall{{Name: "wait", Input: map[string]any{"seconds": 2}}}
		}
		m.heard = true
	}
	var pending []llm.MockCall
	seen := map[string]bool{}
	reqs := f8ReqRe.FindAllStringSubmatch(o.Known, -1)
	sort.SliceStable(reqs, func(i, j int) bool { return reqs[i][1] < reqs[j][1] })
	for _, g := range reqs {
		if paid[g[1]] || seen[g[1]] || g[3] == m.id {
			continue
		}
		seen[g[1]] = true
		var amt int
		_, _ = fmt.Sscan(g[2], &amt) // the request pattern only matches digits here
		pending = append(pending, llm.MockCall{Name: "transfer", Input: map[string]any{"to": g[3], "amount": amt, "idempotency_key": g[1]}})
	}
	if len(pending) > 0 && !strings.Contains(o.Last, "insufficient") {
		return pending[:1]
	}
	if !o.Tools["wait"] || (strings.Contains(o.Last, "All other agents are idle") && len(pending) == 0) {
		return []llm.MockCall{{Name: "done", Input: map[string]any{}}}
	}
	return []llm.MockCall{{Name: "wait", Input: map[string]any{"seconds": 2}}}
}

// f8Order is a transfer as an agent asked for it, before the ledger checks it.
type f8Order struct {
	From, To string
	Amount   int
	Key      string
}

// f8OrderInput reads a model-written transfer. The amount must be a whole
// JSON number: a fractional or out-of-range amount has no exact ledger
// meaning, so it is refused rather than rounded.
func f8OrderInput(input json.RawMessage) (f8Order, error) {
	in, err := llm.ParseInput(input)
	if err != nil {
		return f8Order{}, err
	}
	amt := in.Get("amount")
	if amt.Type != gjson.Number || amt.Num != math.Trunc(amt.Num) || math.Abs(amt.Num) > math.MaxInt32 {
		return f8Order{}, errors.New("amount must be a whole number")
	}
	return f8Order{From: in.Get("from").String(), To: in.Get("to").String(), Amount: int(amt.Num), Key: in.Get("idempotency_key").String()}, nil
}
