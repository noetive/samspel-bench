// Package task defines the task families: procedurally generated instances,
// the environment agents act in, and the verifier that scores the result.
package task

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/goccy/go-json"

	"github.com/noetive/samspel-bench/internal/llm"
	"github.com/noetive/samspel-bench/internal/trace"
)

// OracleID is the single agent of the oracle control.
const OracleID = "solo"

// Params are family parameters from the config file (JSON numbers).
type Params map[string]any

func (p Params) Int(k string, def int) int {
	switch v := p[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func (p Params) Float(k string, def float64) float64 {
	switch v := p[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return def
}

// EnvConfig configures one run's environment.
type EnvConfig struct {
	Decision  string  // "single" (first submission ends the task) or "all" (every honest agent submits)
	LostReply float64 // probability a side-effecting tool applies its effect but loses the reply
	Oracle    bool    // one agent with every view and permission
	Seed      uint64
}

// Env is the task state agents act on through task tools. Safe for
// concurrent use.
type Env interface {
	Tools(agent string) []llm.Tool
	// Call runs a task tool. final reports that the agent has finished.
	Call(agent, name string, input json.RawMessage) (result string, final bool)
	// Done reports that the task is over for everyone.
	Done() bool
}

// Marker is a trackable value: a fact, a planted false value, or a
// requirement. It is present in a text when every phrase occurs in it
// (case-insensitive). With Window > 0, the other phrases must occur within
// Window characters of an occurrence of the first phrase, which keeps short
// values (numbers, colors) from matching unrelated parts of a message.
type Marker struct {
	ID      string
	Kind    string // key, distractor, false, value
	Phrases []string
	Window  int
}

func (m Marker) In(text string) bool {
	if len(m.Phrases) == 0 {
		return false
	}
	lt := strings.ToLower(text)
	if m.Window <= 0 {
		for _, p := range m.Phrases {
			if !strings.Contains(lt, p) {
				return false
			}
		}
		return true
	}
	first := m.Phrases[0]
	for off := 0; ; {
		i := strings.Index(lt[off:], first)
		if i < 0 {
			return false
		}
		i += off
		lo, hi := i-m.Window, i+len(first)+m.Window
		if lo < 0 {
			lo = 0
		}
		if hi > len(lt) {
			hi = len(lt)
		}
		win := lt[lo:hi]
		ok := true
		for _, p := range m.Phrases[1:] {
			if !containsWord(win, p) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
		off = i + 1
	}
}

// containsWord finds p in s with non-alphanumeric characters (or the ends of
// s) on both sides, so "5" does not match inside "15".
func containsWord(s, p string) bool {
	for off := 0; ; {
		i := strings.Index(s[off:], p)
		if i < 0 {
			return false
		}
		i += off
		j := i + len(p)
		if (i == 0 || !isAlnum(s[i-1])) && (j == len(s) || !isAlnum(s[j])) {
			return true
		}
		off = i + 1
	}
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// Outcome is the verifier's verdict.
type Outcome struct {
	Success bool
	Score   float64
	Metrics map[string]float64
}

// Instance is one generated task.
type Instance interface {
	Agents() []string         // honest agent IDs, in chain order where it matters
	Briefing() string         // shared task description
	View(agent string) string // private information of one agent
	OracleView() string       // union of all private information
	Chain() []string          // non-nil when agents may only talk to the next in line
	MessageCap() int          // per-message token cap the family requires; 0 for none
	NewEnv(cfg EnvConfig) Env // fresh environment for one run
	Verify(env Env, honest []string, tr *trace.Trace) Outcome
	Markers() []Marker
	FalseClaims() []string   // lines a confident-wrong adversary asserts
	AdversaryTarget() string // the wrong answer those claims point to, if any
	MockPolicy(agent string) llm.MockPolicy
}

// Family generates instances.
type Family interface {
	Name() string
	Description() string
	MinAgents() int
	Generate(seed uint64, p Params, ids []string) (Instance, error)
}

// Registry lists every implemented family.
var Registry = map[string]Family{
	"F1": f1{},
	"F5": f5{},
	"F8": f8{},
}

// Names returns registered family names in order.
func Names() []string {
	var out []string
	for k := range Registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func rngFor(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15)) }

func schema(s string) json.RawMessage { return json.RawMessage(s) }

func norm(s string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(s)), ".\"' ")
}

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }
