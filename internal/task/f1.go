package task

import (
	"fmt"
	"strings"
	"sync"

	"github.com/goccy/go-json"

	"github.com/noetive/samspel-bench/internal/llm"
	"github.com/noetive/samspel-bench/internal/trace"
)

// F1 information pooling: a hidden-profile identification task. Three key
// clues about the culprit are held by three different agents. Each clue
// alone and each pair leave at least two suspects; all three leave exactly
// one. Everything else is distractors.
type f1 struct{}

func (f1) Name() string   { return "F1" }
func (f1) MinAgents() int { return 3 }
func (f1) Description() string {
	return "Information pooling: join clues spread across agents to identify one suspect"
}

type f1Attr struct {
	key    string
	values []string
	phrase func(string) string
	marker func(string) []string
}

var f1Attrs = []f1Attr{
	{"shift", []string{"day", "night", "swing"}, func(v string) string { return "works the " + v + " shift" }, func(v string) []string { return []string{v + " shift"} }},
	{"floor", []string{"1", "2", "3", "4"}, func(v string) string { return "works on floor " + v }, func(v string) []string { return []string{"floor " + v} }},
	{"badge", []string{"red", "blue", "green", "yellow"}, func(v string) string { return "carries a " + v + " badge" }, func(v string) []string { return []string{v + " badge"} }},
	{"team", []string{"billing", "platform", "research", "support"}, func(v string) string { return "is on the " + v + " team" }, func(v string) []string { return []string{v + " team"} }},
	{"vehicle", []string{"bicycle", "car", "bus", "train"}, func(v string) string { return "commutes by " + v }, func(v string) []string { return []string{"by " + v} }},
}

var f1Names = strings.Fields("Avery Blake Casey Dana Ellis Finley Gray Harper Indy Jordan Kendall Logan Morgan Noel Oakley Parker Quinn Reese Sage Taylor Umber Vale Wren Xen Yael Zion Arden Briar Corin Dallas")

type f1Distractor struct{ text, marker string }

var f1Distractors = []f1Distractor{
	{"Camera 4 in the lobby was offline for twenty minutes.", "camera 4"},
	{"The server room door log shows a gap after midnight.", "door log"},
	{"A coffee cup was found next to the rack in row C.", "coffee cup"},
	{"The alert was acknowledged eleven minutes after it fired.", "eleven minutes"},
	{"The backup job had been paused the previous week.", "backup job"},
	{"Two visitors signed in at reception that evening.", "two visitors"},
	{"The power supply in rack 12 was replaced last month.", "rack 12"},
	{"An unfamiliar laptop connected to the guest network.", "guest network"},
	{"The fire alarm test was rescheduled to Thursday.", "fire alarm"},
	{"The incident ticket was opened by the on-call engineer.", "incident ticket"},
	{"A printer jam was reported near the kitchen.", "printer jam"},
	{"The elevator was out of service for maintenance.", "elevator"},
	{"Someone requested a password reset at 21:40.", "password reset"},
	{"The cleaning crew left earlier than usual.", "cleaning crew"},
	{"A delivery van was parked at the loading dock.", "loading dock"},
	{"The monitoring dashboard showed a brief latency spike.", "latency spike"},
	{"A contractor's access expired the week before.", "contractor"},
	{"The weekly change freeze started on Friday.", "change freeze"},
	{"A spare key was missing from the key cabinet.", "key cabinet"},
	{"The network switch firmware was updated recently.", "switch firmware"},
}

type f1Suspect struct {
	name  string
	attrs []string
}

type f1Inst struct {
	ids         []string
	sus         []f1Suspect
	culprit     int
	keyAttrs    []int
	views       map[string][]string
	markers     []Marker
	falseClaims []string
	falseTarget string
	falseAttr   int
}

func f1Clue(a int, v string) string {
	return fmt.Sprintf("Evidence: the person responsible %s.", f1Attrs[a].phrase(v))
}

func f1Count(sus []f1Suspect, culprit int, attrs []int) int {
	n := 0
	for _, s := range sus {
		ok := true
		for _, a := range attrs {
			if s.attrs[a] != sus[culprit].attrs[a] {
				ok = false
				break
			}
		}
		if ok {
			n++
		}
	}
	return n
}

func (f1) Generate(seed uint64, p Params, ids []string) (Instance, error) {
	n := len(ids)
	if n < 3 {
		return nil, errorf("F1 needs at least 3 agents, got %d", n)
	}
	k := p.Int("suspects", 5)
	cpa := p.Int("clues_per_agent", 4)
	if k < 3 || k > len(f1Names) {
		return nil, errorf("F1 suspects must be between 3 and %d", len(f1Names))
	}
	r := rngFor(seed)
	for try := 0; try < 20000; try++ {
		sus := make([]f1Suspect, k)
		perm := r.Perm(len(f1Names))
		for i := range sus {
			sus[i].name = f1Names[perm[i]]
			sus[i].attrs = make([]string, len(f1Attrs))
			for a := range f1Attrs {
				vs := f1Attrs[a].values
				sus[i].attrs[a] = vs[r.IntN(len(vs))]
			}
		}
		culprit := r.IntN(k)
		keys := r.Perm(len(f1Attrs))[:3]
		ok := f1Count(sus, culprit, keys) == 1
		for i := 0; ok && i < 3; i++ {
			if f1Count(sus, culprit, []int{keys[i]}) < 2 {
				ok = false
			}
			for j := i + 1; ok && j < 3; j++ {
				if f1Count(sus, culprit, []int{keys[i], keys[j]}) < 2 {
					ok = false
				}
			}
		}
		if !ok {
			continue
		}
		in := &f1Inst{ids: ids, sus: sus, culprit: culprit, keyAttrs: keys, views: map[string][]string{}}
		holders := r.Perm(n)[:3]
		for i, h := range holders {
			a := keys[i]
			v := sus[culprit].attrs[a]
			in.views[ids[h]] = append(in.views[ids[h]], f1Clue(a, v))
			in.markers = append(in.markers, Marker{ID: "key-" + f1Attrs[a].key, Kind: "key", Phrases: f1Attrs[a].marker(v)})
		}
		dperm := r.Perm(len(f1Distractors))
		di := 0
		seen := map[int]bool{}
		for _, id := range ids {
			for len(in.views[id]) < cpa {
				d := dperm[di%len(dperm)]
				di++
				in.views[id] = append(in.views[id], f1Distractors[d].text)
				if !seen[d] {
					seen[d] = true
					in.markers = append(in.markers, Marker{ID: fmt.Sprintf("d%02d", d), Kind: "distractor", Phrases: []string{f1Distractors[d].marker}})
				}
			}
			r.Shuffle(len(in.views[id]), func(i, j int) { in.views[id][i], in.views[id][j] = in.views[id][j], in.views[id][i] })
		}
		in.planFalse()
		return in, nil
	}
	return nil, errorf("F1: could not generate a valid instance for seed %d", seed)
}

// planFalse picks a false clue that swaps one key attribute so that,
// combined with the other two true clues, it points at exactly one innocent
// suspect. That suspect is the adversary target for contamination metrics.
func (in *f1Inst) planFalse() {
	c := in.sus[in.culprit]
	in.falseAttr = -1
	for i, a := range in.keyAttrs {
		for _, v := range f1Attrs[a].values {
			if v == c.attrs[a] {
				continue
			}
			var hits []int
			for si, s := range in.sus {
				ok := s.attrs[a] == v
				for j, b := range in.keyAttrs {
					if j != i && s.attrs[b] != c.attrs[b] {
						ok = false
					}
				}
				if ok {
					hits = append(hits, si)
				}
			}
			if len(hits) == 1 {
				in.falseClaims = []string{f1Clue(a, v)}
				in.falseTarget = in.sus[hits[0]].name
				in.falseAttr = a
				in.markers = append(in.markers, Marker{ID: "false-" + f1Attrs[a].key, Kind: "false", Phrases: f1Attrs[a].marker(v)})
				return
			}
		}
	}
	// No clean target: assert a value that only excludes the culprit.
	a := in.keyAttrs[0]
	for _, v := range f1Attrs[a].values {
		if v != c.attrs[a] {
			in.falseClaims = []string{f1Clue(a, v)}
			in.falseAttr = a
			in.markers = append(in.markers, Marker{ID: "false-" + f1Attrs[a].key, Kind: "false", Phrases: f1Attrs[a].marker(v)})
			return
		}
	}
}

func (in *f1Inst) Agents() []string { return in.ids }
func (in *f1Inst) Chain() []string  { return nil }
func (in *f1Inst) MessageCap() int  { return 0 }

func (in *f1Inst) roster() string {
	var sb strings.Builder
	for _, s := range in.sus {
		fmt.Fprintf(&sb, "- %s:", s.name)
		for a, attr := range f1Attrs {
			fmt.Fprintf(&sb, " %s=%s", attr.key, s.attrs[a])
			if a < len(f1Attrs)-1 {
				sb.WriteString(",")
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (in *f1Inst) Briefing() string {
	return "An incident happened and exactly one of the suspects below is responsible. " +
		"Evidence about the responsible person is split across the team, and no single agent has enough to decide. " +
		"Identify the responsible suspect and submit their name with the submit tool.\n\nSuspects:\n" + in.roster()
}

func (in *f1Inst) View(agent string) string {
	return "Your evidence:\n- " + strings.Join(in.views[agent], "\n- ")
}

func (in *f1Inst) OracleView() string {
	var all []string
	for _, id := range in.ids {
		all = append(all, in.views[id]...)
	}
	return "All evidence:\n- " + strings.Join(all, "\n- ")
}

func (in *f1Inst) Markers() []Marker       { return in.markers }
func (in *f1Inst) FalseClaims() []string   { return in.falseClaims }
func (in *f1Inst) AdversaryTarget() string { return in.falseTarget }

// solve is the reference solver: apply every clue found in text.
func (in *f1Inst) solve(text string) string {
	lt := strings.ToLower(text)
	var cands []int
	for i, s := range in.sus {
		ok := true
		for a, attr := range f1Attrs {
			for _, v := range attr.values {
				if s.attrs[a] != v && strings.Contains(lt, "person responsible "+attr.phrase(v)) {
					ok = false
				}
			}
		}
		if ok {
			cands = append(cands, i)
		}
	}
	if len(cands) == 0 {
		return in.sus[0].name
	}
	return in.sus[cands[0]].name
}

type f1Env struct {
	mu    sync.Mutex
	in    *f1Inst
	cfg   EnvConfig
	subs  map[string]string
	order []string
}

func (in *f1Inst) NewEnv(cfg EnvConfig) Env {
	return &f1Env{in: in, cfg: cfg, subs: map[string]string{}}
}

func (e *f1Env) Tools(agent string) []llm.Tool {
	desc := "Submit the name of the responsible suspect. The first submission from any agent is final for the whole team and ends the task."
	if e.cfg.Decision == "all" && !e.cfg.Oracle {
		desc = "Submit the name of the responsible suspect. Every agent must submit, and the team succeeds only if every agent submits the correct name. Your submission is final."
	}
	return []llm.Tool{{Name: "submit", Description: desc,
		InputSchema: schema(`{"type":"object","properties":{"answer":{"type":"string","description":"suspect name"}},"required":["answer"]}`)}}
}

func (e *f1Env) Call(agent, name string, input json.RawMessage) (string, bool) {
	if name != "submit" {
		return "error: unknown tool " + name, false
	}
	in, err := llm.ParseInput(input)
	answer := in.Get("answer").String()
	if err != nil || strings.TrimSpace(answer) == "" {
		return "error: provide a non-empty answer", false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if (e.cfg.Decision != "all" || e.cfg.Oracle) && len(e.order) > 0 {
		return "the team's answer was already submitted; the task is over", true
	}
	if _, ok := e.subs[agent]; ok {
		return "you already submitted", true
	}
	e.subs[agent] = answer
	e.order = append(e.order, agent)
	return "answer recorded", true
}

func (e *f1Env) Done() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.Decision != "all" || e.cfg.Oracle {
		return len(e.order) > 0
	}
	for _, id := range e.in.ids {
		if _, ok := e.subs[id]; !ok {
			return false
		}
	}
	return true
}

func (in *f1Inst) Verify(env Env, honest []string, tr *trace.Trace) Outcome {
	e := env.(*f1Env)
	e.mu.Lock()
	defer e.mu.Unlock()
	want := norm(in.sus[in.culprit].name)
	m := map[string]float64{}
	var ok bool
	if e.cfg.Decision == "all" && !e.cfg.Oracle {
		ok = true
		agree := true
		first := ""
		for i, id := range honest {
			a, has := e.subs[id]
			if !has {
				ok, agree = false, false
				continue
			}
			if norm(a) != want {
				ok = false
			}
			if i == 0 {
				first = norm(a)
			} else if norm(a) != first {
				agree = false
			}
		}
		m["agreement"] = b2f(agree)
	} else {
		ok = len(e.order) > 0 && norm(e.subs[e.order[0]]) == want
	}
	m["submitted"] = float64(len(e.order))
	return Outcome{Success: ok, Score: b2f(ok), Metrics: m}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

type f1Mock struct {
	in *f1Inst
	id string
}

func (in *f1Inst) MockPolicy(agent string) llm.MockPolicy { return &f1Mock{in: in, id: agent} }

// Next shares its evidence, listens until the team is idle, then applies every
// clue it has seen. A wait returns on the first message, so a fixed number of
// waits can submit before the last peer's evidence lands. It takes claims at
// face value, so it is fooled by false ones.
func (m *f1Mock) Next(o llm.MockObs) []llm.MockCall {
	switch {
	case o.Step == 0 && o.Tools["broadcast"]:
		return []llm.MockCall{{Name: "broadcast", Input: map[string]any{"text": strings.Join(m.in.views[m.id], "\n")}}}
	case o.Step < 10 && o.Tools["wait"] && !strings.Contains(o.Last, "All other agents are idle"):
		return []llm.MockCall{{Name: "wait", Input: map[string]any{"seconds": 2}}}
	default:
		return []llm.MockCall{{Name: "submit", Input: map[string]any{"answer": m.in.solve(o.Known)}}}
	}
}
