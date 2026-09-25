package task

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/goccy/go-json"

	"github.com/noetive/samspel-bench/internal/llm"
	"github.com/noetive/samspel-bench/internal/trace"
)

// F5 relay: the first agent in a chain holds a specification, the last must
// answer questions about it, and each agent can only message the next one,
// with a per-message token cap. Measures compression and handoff fidelity.
type f5 struct{}

func (f5) Name() string   { return "F5" }
func (f5) MinAgents() int { return 2 }
func (f5) Description() string {
	return "Relay: pass a specification down a chain under a per-message token cap"
}

var f5Components = []string{
	"export job", "billing sync", "search indexer", "image resizer", "audit logger",
	"email sender", "cache warmer", "report builder", "payment reconciler", "session sweeper",
	"metrics collector", "backup agent", "feature flag service", "token refresher", "invoice generator",
	"geo resolver", "queue drainer", "schema migrator", "webhook dispatcher", "thumbnail service",
}

type f5Prop struct {
	name string
	kw   string // short keyword agents are likely to keep when compressing
	gen  func(r interface{ IntN(int) int }) string
}

var f5Props = []f5Prop{
	{"retry limit", "retry", func(r interface{ IntN(int) int }) string { return fmt.Sprint(2 + r.IntN(8)) }},
	{"timeout in seconds", "timeout", func(r interface{ IntN(int) int }) string { return fmt.Sprint(10 + 5*r.IntN(35)) }},
	{"owner team", "owner", func(r interface{ IntN(int) int }) string {
		return []string{"atlas", "borealis", "cobalt", "delta", "ember", "fjord", "granite", "harbor"}[r.IntN(8)]
	}},
	{"deployment region", "region", func(r interface{ IntN(int) int }) string {
		return []string{"eu-north-1", "eu-west-3", "us-east-2", "ap-south-1", "sa-east-1", "ca-central-1"}[r.IntN(6)]
	}},
	{"batch size", "batch", func(r interface{ IntN(int) int }) string { return fmt.Sprint(50 + 25*r.IntN(37)) }},
	{"alert channel", "alert", func(r interface{ IntN(int) int }) string {
		return []string{"#ops-amber", "#ops-violet", "#oncall-teal", "#infra-coral", "#data-slate"}[r.IntN(5)]
	}},
}

type f5Req struct{ comp, prop, val string }

func (q f5Req) line(i int) string {
	return fmt.Sprintf("REQ-%02d: The %s for the %s is %s.", i+1, q.prop, q.comp, q.val)
}

func (q f5Req) question(i int) string {
	return fmt.Sprintf("Q-%02d: What is the %s for the %s?", i+1, q.prop, q.comp)
}

type f5Inst struct {
	ids     []string
	reqs    []f5Req
	cap     int
	pass    float64
	markers []Marker
	falses  []string
}

func (f5) Generate(seed uint64, p Params, ids []string) (Instance, error) {
	if len(ids) < 2 {
		return nil, errorf("F5 needs at least 2 agents")
	}
	R := p.Int("requirements", 40)
	if R < 1 || R > len(f5Components)*len(f5Props) {
		return nil, errorf("F5 requirements must be between 1 and %d", len(f5Components)*len(f5Props))
	}
	r := rngFor(seed)
	in := &f5Inst{ids: ids, cap: p.Int("cap_tokens", 400), pass: p.Float("pass", 0.9)}
	pairs := r.Perm(len(f5Components) * len(f5Props))[:R]
	for i, pi := range pairs {
		q := f5Req{comp: f5Components[pi/len(f5Props)], prop: f5Props[pi%len(f5Props)].name}
		q.val = f5Props[pi%len(f5Props)].gen(r)
		in.reqs = append(in.reqs, q)
		in.markers = append(in.markers, Marker{ID: fmt.Sprintf("REQ-%02d", i+1), Kind: "value", Window: 30,
			Phrases: []string{q.comp, f5Props[pi%len(f5Props)].kw, strings.ToLower(q.val)}})
	}
	for i := 0; i < 3 && i < R; i++ {
		q := in.reqs[i]
		wrong := q.val
		for tries := 0; wrong == q.val && tries < 50; tries++ {
			wrong = f5Props[pairs[i]%len(f5Props)].gen(r)
		}
		if wrong == q.val {
			continue
		}
		f := q
		f.val = wrong
		in.falses = append(in.falses, "Correction: "+f.line(i))
		in.markers = append(in.markers, Marker{ID: fmt.Sprintf("false-REQ-%02d", i+1), Kind: "false", Window: 30,
			Phrases: []string{q.comp, f5Props[pairs[i]%len(f5Props)].kw, strings.ToLower(wrong)}})
	}
	return in, nil
}

func (in *f5Inst) Agents() []string { return in.ids }
func (in *f5Inst) Chain() []string  { return in.ids }
func (in *f5Inst) MessageCap() int  { return in.cap }
func (in *f5Inst) last() string     { return in.ids[len(in.ids)-1] }

func (in *f5Inst) next(id string) string {
	for i, x := range in.ids {
		if x == id && i+1 < len(in.ids) {
			return in.ids[i+1]
		}
	}
	return ""
}

func (in *f5Inst) prev(id string) string {
	for i, x := range in.ids {
		if x == id && i > 0 {
			return in.ids[i-1]
		}
	}
	return ""
}

func (in *f5Inst) spec() string {
	var sb strings.Builder
	sb.WriteString("Specification:\n")
	for i, q := range in.reqs {
		sb.WriteString(q.line(i) + "\n")
	}
	return sb.String()
}

func (in *f5Inst) quiz() string {
	var sb strings.Builder
	sb.WriteString("Questions:\n")
	for i, q := range in.reqs {
		sb.WriteString(q.question(i) + "\n")
	}
	return sb.String()
}

func (in *f5Inst) Briefing() string {
	return fmt.Sprintf("The agents form a chain: %s. Each agent can send messages only to the next agent in the chain, "+
		"and each message is limited to %d tokens. The first agent holds a specification. The last agent must answer "+
		"questions about it with the submit tool. Every other agent calls done after passing information on.",
		strings.Join(in.ids, " -> "), in.cap)
}

func (in *f5Inst) View(agent string) string {
	switch {
	case agent == in.ids[0] && agent == in.last():
		return in.spec() + "\n" + in.quiz()
	case agent == in.ids[0]:
		return in.spec() + fmt.Sprintf("\nPass on what the last agent will need to %s, then call done.", in.next(agent))
	case agent == in.last():
		return fmt.Sprintf("You will receive information from %s. Answer every question below with the submit tool.\n\n", in.prev(agent)) + in.quiz()
	default:
		return fmt.Sprintf("You will receive information from %s. Pass on what the last agent will need to %s, then call done.", in.prev(agent), in.next(agent))
	}
}

func (in *f5Inst) OracleView() string { return in.spec() + "\n" + in.quiz() }

func (in *f5Inst) Markers() []Marker       { return in.markers }
func (in *f5Inst) FalseClaims() []string   { return in.falses }
func (in *f5Inst) AdversaryTarget() string { return "" }

type f5Env struct {
	mu        sync.Mutex
	in        *f5Inst
	cfg       EnvConfig
	answers   map[string]string
	submitted bool
}

func (in *f5Inst) NewEnv(cfg EnvConfig) Env {
	return &f5Env{in: in, cfg: cfg, answers: map[string]string{}}
}

func (e *f5Env) answerer(agent string) bool { return e.cfg.Oracle || agent == e.in.last() }

func (e *f5Env) Tools(agent string) []llm.Tool {
	if e.answerer(agent) {
		return []llm.Tool{{Name: "submit", Description: "Submit answers to the questions. Final; call it once with every answer.",
			InputSchema: schema(`{"type":"object","properties":{"answers":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string","description":"question id, e.g. Q-07"},"value":{"type":"string"}},"required":["id","value"]}}},"required":["answers"]}`)}}
	}
	return []llm.Tool{{Name: "done", Description: "Call when you have passed on everything the next agent needs. Final.",
		InputSchema: schema(`{"type":"object","properties":{}}`)}}
}

func (e *f5Env) Call(agent, name string, input json.RawMessage) (string, bool) {
	switch {
	case name == "done" && !e.answerer(agent):
		return "ok", true
	case name == "submit" && e.answerer(agent):
		in, err := llm.ParseInput(input)
		if err != nil {
			return "error: could not parse answers: " + err.Error(), false
		}
		answers := in.Get("answers").Array()
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.submitted {
			return "already submitted", true
		}
		for _, a := range answers {
			e.answers[strings.ToUpper(strings.TrimSpace(a.Get("id").String()))] = a.Get("value").String()
		}
		e.submitted = true
		return fmt.Sprintf("recorded %d answers", len(answers)), true
	}
	return "error: tool " + name + " is not available to you", false
}

func (e *f5Env) Done() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.submitted
}

func (in *f5Inst) Verify(env Env, honest []string, tr *trace.Trace) Outcome {
	e := env.(*f5Env)
	e.mu.Lock()
	defer e.mu.Unlock()
	correct := 0
	for i, q := range in.reqs {
		if norm(e.answers[fmt.Sprintf("Q-%02d", i+1)]) == norm(q.val) {
			correct++
		}
	}
	score := float64(correct) / float64(len(in.reqs))
	m := map[string]float64{"retention": score}
	if !e.cfg.Oracle {
		for i, id := range in.ids[:len(in.ids)-1] {
			sent := tr.SentBy(id)
			n := 0
			for _, mk := range in.markers {
				if mk.Kind == "value" && mk.In(sent) {
					n++
				}
			}
			m[fmt.Sprintf("hop%d_retention", i+1)] = float64(n) / float64(len(in.reqs))
		}
	}
	return Outcome{Success: score >= in.pass, Score: score, Metrics: m}
}

var f5ReqRe = regexp.MustCompile(`REQ-\d+: The [^\n]*? is [^\n]*?\.`)

type f5Mock struct {
	in   *f5Inst
	id   string
	sent bool
}

func (in *f5Inst) MockPolicy(agent string) llm.MockPolicy { return &f5Mock{in: in, id: agent} }

// Next forwards every requirement line it has seen, split into chunks under
// the cap. The last agent answers from the lines it has received.
func (m *f5Mock) Next(o llm.MockObs) []llm.MockCall {
	lines := uniq(f5ReqRe.FindAllString(o.Known, -1))
	if o.Tools["submit"] {
		if len(lines) < len(m.in.reqs) && o.Step < 4 && o.Tools["wait"] {
			return []llm.MockCall{{Name: "wait", Input: map[string]any{"seconds": 3}}}
		}
		var ans []map[string]string
		for i, q := range m.in.reqs {
			key := fmt.Sprintf("The %s for the %s is ", q.prop, q.comp)
			for _, l := range lines {
				if j := strings.Index(l, key); j >= 0 {
					ans = append(ans, map[string]string{"id": fmt.Sprintf("Q-%02d", i+1), "value": strings.TrimSuffix(l[j+len(key):], ".")})
					break
				}
			}
		}
		return []llm.MockCall{{Name: "submit", Input: map[string]any{"answers": ans}}}
	}
	if m.sent || !o.Tools["send"] {
		return []llm.MockCall{{Name: "done", Input: map[string]any{}}}
	}
	if len(lines) == 0 && o.Step < 4 {
		return []llm.MockCall{{Name: "wait", Input: map[string]any{"seconds": 3}}}
	}
	m.sent = true
	var calls []llm.MockCall
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			calls = append(calls, llm.MockCall{Name: "send", Input: map[string]any{"to": m.in.next(m.id), "text": strings.Join(cur, "\n")}})
			cur = nil
		}
	}
	for _, l := range lines {
		if (len(strings.Join(append(cur, l), "\n"))+3)/4 > m.in.cap-8 {
			flush()
		}
		cur = append(cur, l)
	}
	flush()
	return calls
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
