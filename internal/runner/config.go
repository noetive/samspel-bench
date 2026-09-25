// Package runner expands a benchmark config into jobs and runs them in
// parallel against a shared, rate-limited model client.
package runner

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"

	"github.com/goccy/go-json"

	"github.com/noetive/samspel-bench/internal/agent"
	"github.com/noetive/samspel-bench/internal/llm"
	"github.com/noetive/samspel-bench/internal/task"
)

// Controls implemented by the runner.
var Controls = []string{"team", "nocomm", "oracle", "broadcast_all", "ablation"}

// Channel sets channel faults for a condition.
type Channel struct {
	Drop       float64 `json:"drop,omitempty"`
	Duplicate  float64 `json:"duplicate,omitempty"`
	MaxDelayMS int     `json:"max_delay_ms,omitempty"`
	LostReply  float64 `json:"lost_reply,omitempty"`
}

// Byzantine adds scripted faulty peers on top of the honest team.
type Byzantine struct {
	Count  int    `json:"count,omitempty"`
	Script string `json:"script,omitempty"`
}

// Condition is one point on the experimental axes.
type Condition struct {
	Name             string    `json:"name"`
	N                int       `json:"n,omitempty"`        // honest team size; 0 uses the family default
	Topology         string    `json:"topology,omitempty"` // free (default) or star
	Decision         string    `json:"decision,omitempty"` // single (default) or all
	Awareness        bool      `json:"awareness,omitempty"`
	BudgetTokens     int       `json:"budget_tokens,omitempty"`
	MaxMessageTokens int       `json:"max_message_tokens,omitempty"`
	Channel          Channel   `json:"channel,omitempty"`
	Byzantine        Byzantine `json:"byzantine,omitempty"`
}

// FamilySpec selects a family, its parameters and instance count.
type FamilySpec struct {
	Name      string      `json:"name"`
	Params    task.Params `json:"params,omitempty"`
	N         int         `json:"n,omitempty"` // default honest team size
	Instances int         `json:"instances"`
}

// AgentSpec configures the reference agents.
type AgentSpec struct {
	MaxTurns       int      `json:"max_turns,omitempty"`
	MaxTokens      int      `json:"max_tokens,omitempty"`
	WaitTimeoutSec int      `json:"wait_timeout_sec,omitempty"`
	RunTimeoutSec  int      `json:"run_timeout_sec,omitempty"`
	Temperature    *float64 `json:"temperature,omitempty"`
}

// Config is the whole benchmark run.
type Config struct {
	Models      []string              `json:"models"`
	Families    []FamilySpec          `json:"families"`
	Conditions  []Condition           `json:"conditions"`
	Controls    []string              `json:"controls,omitempty"`
	Repeats     int                   `json:"repeats,omitempty"`
	Seed        uint64                `json:"seed,omitempty"`
	Agent       AgentSpec             `json:"agent,omitempty"`
	Parallel    int                   `json:"parallel,omitempty"`
	Limits      llm.Limits            `json:"limits,omitempty"`
	ModelLimits map[string]llm.Limits `json:"model_limits,omitempty"`
}

// LoadConfig reads, defaults and validates a config file.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.defaults()
	return &c, c.validate()
}

func (c *Config) defaults() {
	if len(c.Controls) == 0 {
		c.Controls = []string{"team", "nocomm", "oracle"}
	}
	if c.Repeats <= 0 {
		c.Repeats = 1
	}
	if len(c.Conditions) == 0 {
		c.Conditions = []Condition{{Name: "core"}}
	}
	if c.Agent.MaxTurns <= 0 {
		c.Agent.MaxTurns = 30
	}
	if c.Agent.MaxTokens <= 0 {
		c.Agent.MaxTokens = 1024
	}
	if c.Agent.WaitTimeoutSec <= 0 {
		c.Agent.WaitTimeoutSec = 20
	}
	if c.Agent.RunTimeoutSec <= 0 {
		c.Agent.RunTimeoutSec = 900
	}
	if c.Parallel <= 0 {
		c.Parallel = 32
	}
	if c.Limits.MaxInflight <= 0 {
		c.Limits.MaxInflight = 64
	}
}

func (c *Config) validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("config: at least one model is required")
	}
	okCtl := map[string]bool{}
	for _, x := range Controls {
		okCtl[x] = true
	}
	for _, x := range c.Controls {
		if !okCtl[x] {
			return fmt.Errorf("config: unknown control %q (have %v)", x, Controls)
		}
	}
	okScript := map[string]bool{}
	for _, s := range agent.Scripts {
		okScript[s] = true
	}
	names := map[string]bool{}
	for _, cond := range c.Conditions {
		if cond.Name == "" || names[cond.Name] {
			return fmt.Errorf("config: conditions need unique names")
		}
		names[cond.Name] = true
		if cond.Byzantine.Count > 0 && !okScript[cond.Byzantine.Script] {
			return fmt.Errorf("config: condition %s: unknown adversary script %q (have %v)", cond.Name, cond.Byzantine.Script, agent.Scripts)
		}
		if cond.Topology != "" && cond.Topology != "free" && cond.Topology != "star" {
			return fmt.Errorf("config: condition %s: topology must be free or star", cond.Name)
		}
		if cond.Decision != "" && cond.Decision != "single" && cond.Decision != "all" {
			return fmt.Errorf("config: condition %s: decision must be single or all", cond.Name)
		}
	}
	for _, f := range c.Families {
		fam, ok := task.Registry[f.Name]
		if !ok {
			return fmt.Errorf("config: unknown family %q (have %v)", f.Name, task.Names())
		}
		if f.Instances <= 0 {
			return fmt.Errorf("config: family %s needs instances > 0", f.Name)
		}
		if f.N != 0 && f.N < fam.MinAgents() {
			return fmt.Errorf("config: family %s needs n >= %d", f.Name, fam.MinAgents())
		}
	}
	return nil
}

// Job is one run: model x family x condition x control x instance x repeat.
type Job struct {
	ID           string      `json:"id"`
	Model        string      `json:"model"`
	Family       string      `json:"family"`
	Params       task.Params `json:"-"`
	N            int         `json:"n"`
	Cond         Condition   `json:"-"`
	Control      string      `json:"control"`
	Instance     int         `json:"instance"`
	InstanceSeed uint64      `json:"instance_seed"`
	Repeat       int         `json:"repeat"`
}

func hash64(parts ...any) uint64 {
	h := sha1.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%v|", p) // a hash never returns a write error
	}
	return binary.BigEndian.Uint64(h.Sum(nil)[:8])
}

// Expand lists every job. Instance seeds depend only on the base seed,
// family and instance index, so controls, models, conditions and repeats
// are paired on the same instances. Jobs are shuffled deterministically so
// load spreads across models and families.
func Expand(c *Config) []Job {
	var jobs []Job
	for _, m := range c.Models {
		for _, f := range c.Families {
			for _, cond := range c.Conditions {
				n := f.N
				if cond.N > 0 {
					n = cond.N
				}
				if n == 0 {
					n = map[string]int{"F1": 4, "F5": 5, "F8": 4}[f.Name]
				}
				for _, ctl := range c.Controls {
					for i := 0; i < f.Instances; i++ {
						seed := hash64(c.Seed, f.Name, i)
						for r := 0; r < c.Repeats; r++ {
							id := hash64(m, f.Name, f.Params, n, cond.Name, ctl, seed, r)
							jobs = append(jobs, Job{
								ID: hex.EncodeToString(binary.BigEndian.AppendUint64(nil, id)), Model: m, Family: f.Name, Params: f.Params,
								N: n, Cond: cond, Control: ctl, Instance: i, InstanceSeed: seed, Repeat: r,
							})
						}
					}
				}
			}
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	r := rand.New(rand.NewPCG(c.Seed, 7))
	r.Shuffle(len(jobs), func(i, j int) { jobs[i], jobs[j] = jobs[j], jobs[i] })
	return jobs
}
