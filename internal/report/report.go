// Package report aggregates results.jsonl into the Samspel scorecard:
// success per control, collaboration gain G with paired bootstrap
// confidence intervals over instances, cost, and secondary metrics.
package report

import (
	"bufio"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"

	"github.com/goccy/go-json"

	"github.com/noetive/samspel-bench/internal/runner"
)

// Load reads results files in order, keeping the latest line per job across
// all of them, so a later run of the same job replaces an earlier one.
func Load(paths ...string) ([]runner.Result, error) {
	byID := map[string]runner.Result{}
	var order []string
	for _, path := range paths {
		if err := loadInto(path, byID, &order); err != nil {
			return nil, err
		}
	}
	out := make([]runner.Result, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

func loadInto(path string, byID map[string]runner.Result, order *[]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // read-only: a close error cannot lose data
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r runner.Result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.JobID == "" {
			continue
		}
		if _, ok := byID[r.JobID]; !ok {
			*order = append(*order, r.JobID)
		}
		byID[r.JobID] = r
	}
	return sc.Err()
}

// Row is one scorecard line (model x family x condition).
type Row struct {
	Model     string             `json:"model"`
	Family    string             `json:"family"`
	Condition string             `json:"condition"`
	Instances int                `json:"instances"`
	S         map[string]float64 `json:"success"` // per control
	STeamCI   [2]float64         `json:"success_team_ci"`
	G         float64            `json:"gain"`
	GCI       [2]float64         `json:"gain_ci"`
	Tokens    float64            `json:"tokens_per_team_run"`
	Messages  float64            `json:"messages_per_team_run"`
	Errors    int                `json:"errors"`
	Metrics   map[string]float64 `json:"team_metrics"`
}

type group struct {
	model, family, cond string
	// seed -> control -> scores (one per repeat)
	bySeed map[uint64]map[string][]float64
	team   []runner.Result
	errors int
}

// Build computes the scorecard.
func Build(results []runner.Result, boot int) []Row {
	groups := map[string]*group{}
	var keys []string
	for _, r := range results {
		k := r.Model + "|" + r.Family + "|" + r.Condition
		g, ok := groups[k]
		if !ok {
			g = &group{model: r.Model, family: r.Family, cond: r.Condition, bySeed: map[uint64]map[string][]float64{}}
			groups[k] = g
			keys = append(keys, k)
		}
		if r.Error != "" {
			g.errors++
			continue // infrastructure failures are not agent failures
		}
		if g.bySeed[r.Seed] == nil {
			g.bySeed[r.Seed] = map[string][]float64{}
		}
		g.bySeed[r.Seed][r.Control] = append(g.bySeed[r.Seed][r.Control], b2f(r.Success))
		if r.Control == "team" {
			g.team = append(g.team, r)
		}
	}
	sort.Strings(keys)
	var rows []Row
	for _, k := range keys {
		g := groups[k]
		var seeds []uint64
		for s := range g.bySeed {
			seeds = append(seeds, s)
		}
		sort.Slice(seeds, func(i, j int) bool { return seeds[i] < seeds[j] })
		row := Row{Model: g.model, Family: g.family, Condition: g.cond, Instances: len(seeds), S: map[string]float64{}, Errors: g.errors, Metrics: map[string]float64{}}
		for _, c := range runner.Controls {
			if v, ok := meanControl(g, seeds, c); ok {
				row.S[c] = v
			}
		}
		row.G = gain(g, seeds)
		row.STeamCI, row.GCI = bootstrap(g, seeds, boot)
		var tok, msg float64
		sums, counts := map[string]float64{}, map[string]float64{}
		for _, r := range g.team {
			tok += float64(r.Usage.Total())
			msg += float64(r.Messages)
			for mk, v := range r.Metrics {
				sums[mk] += v
				counts[mk]++
			}
		}
		if n := float64(len(g.team)); n > 0 {
			row.Tokens, row.Messages = tok/n, msg/n
		}
		for mk := range sums {
			row.Metrics[mk] = sums[mk] / counts[mk]
		}
		rows = append(rows, row)
	}
	return rows
}

func meanControl(g *group, seeds []uint64, c string) (float64, bool) {
	var sum float64
	n := 0
	for _, s := range seeds {
		if v, ok := g.bySeed[s][c]; ok && len(v) > 0 {
			sum += mean(v)
			n++
		}
	}
	if n == 0 {
		return math.NaN(), false
	}
	return sum / float64(n), true
}

// MarshalJSON writes an undefined statistic as null. G and the intervals are
// NaN when too few instances are paired or nocomm leaves no headroom below
// oracle, and JSON has no NaN.
func (r Row) MarshalJSON() ([]byte, error) {
	type row Row
	return json.Marshal(struct {
		row
		STeamCI [2]*float64 `json:"success_team_ci"`
		G       *float64    `json:"gain"`
		GCI     [2]*float64 `json:"gain_ci"`
	}{row(r), definedPair(r.STeamCI), defined(r.G), definedPair(r.GCI)})
}

func defined(x float64) *float64 {
	if math.IsNaN(x) {
		return nil
	}
	return &x
}

func definedPair(c [2]float64) [2]*float64 { return [2]*float64{defined(c[0]), defined(c[1])} }

// gain is G = (S_team - S_nocomm) / (S_oracle - S_nocomm), computed only over
// instances that have all three controls.
func gain(g *group, seeds []uint64) float64 {
	var paired []uint64
	for _, s := range seeds {
		m := g.bySeed[s]
		if len(m["team"]) > 0 && len(m["nocomm"]) > 0 && len(m["oracle"]) > 0 {
			paired = append(paired, s)
		}
	}
	if len(paired) == 0 {
		return math.NaN()
	}
	t, _ := meanControl(g, paired, "team")
	n, _ := meanControl(g, paired, "nocomm")
	o, _ := meanControl(g, paired, "oracle")
	if o-n <= 1e-9 {
		return math.NaN()
	}
	return (t - n) / (o - n)
}

// minCIInstances is the fewest instances an interval is reported for. Below
// it, resampling a handful of identical outcomes yields a zero-width interval
// that reads as certainty.
const minCIInstances = 10

// bootstrap resamples instances with replacement, keeping controls paired.
func bootstrap(g *group, seeds []uint64, B int) ([2]float64, [2]float64) {
	nan := [2]float64{math.NaN(), math.NaN()}
	if len(seeds) < minCIInstances || B <= 0 {
		return nan, nan
	}
	r := rand.New(rand.NewPCG(uint64(len(seeds)), 99))
	var ts, gs []float64
	sample := make([]uint64, len(seeds))
	for i := 0; i < B; i++ {
		for k := range sample {
			sample[k] = seeds[r.IntN(len(seeds))]
		}
		if v, ok := meanControl(g, sample, "team"); ok {
			ts = append(ts, v)
		}
		if v := gain(g, sample); !math.IsNaN(v) {
			gs = append(gs, v)
		}
	}
	return pct(ts), pct(gs)
}

func pct(xs []float64) [2]float64 {
	if len(xs) < 10 {
		return [2]float64{math.NaN(), math.NaN()}
	}
	sort.Float64s(xs)
	return [2]float64{xs[int(0.025*float64(len(xs)))], xs[int(0.975*float64(len(xs)-1))]}
}

func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func f2(x float64) string {
	if math.IsNaN(x) {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", x)
}

func ci(x float64, c [2]float64) string {
	if math.IsNaN(c[0]) {
		return f2(x)
	}
	return fmt.Sprintf("%s [%s, %s]", f2(x), f2(c[0]), f2(c[1]))
}

func sOr(m map[string]float64, k string) float64 {
	if v, ok := m[k]; ok {
		return v
	}
	return math.NaN()
}

// Markdown renders the scorecard.
func Markdown(rows []Row) string {
	var sb strings.Builder
	sb.WriteString("# Samspel results\n\n")
	sb.WriteString("S is the success rate per control. G = (S_team - S_nocomm) / (S_oracle - S_nocomm), paired by instance. ")
	sb.WriteString("Brackets are 95% bootstrap intervals over instances. Tokens and messages are per team run. ")
	sb.WriteString("Runs with infrastructure errors are excluded and counted in Errors.\n\n")
	sb.WriteString("| Model | Family | Condition | Instances | S team | S nocomm | S oracle | G | S broadcast_all | S ablation | Tokens | Messages | Errors |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "| %s | %s | %s | %d | %s | %s | %s | %s | %s | %s | %.0f | %.1f | %d |\n",
			r.Model, r.Family, r.Condition, r.Instances, ci(sOr(r.S, "team"), r.STeamCI), f2(sOr(r.S, "nocomm")), f2(sOr(r.S, "oracle")),
			ci(r.G, r.GCI), f2(sOr(r.S, "broadcast_all")), f2(sOr(r.S, "ablation")), r.Tokens, r.Messages, r.Errors)
	}
	byFam := map[string][]Row{}
	var fams []string
	for _, r := range rows {
		if _, ok := byFam[r.Family]; !ok {
			fams = append(fams, r.Family)
		}
		byFam[r.Family] = append(byFam[r.Family], r)
	}
	sort.Strings(fams)
	for _, f := range fams {
		keySet := map[string]bool{}
		for _, r := range byFam[f] {
			for k := range r.Metrics {
				keySet[k] = true
			}
		}
		if len(keySet) == 0 {
			continue
		}
		var ks []string
		for k := range keySet {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		fmt.Fprintf(&sb, "\n## %s secondary metrics (team control, means)\n\n| Model | Condition | %s |\n|%s\n", f, strings.Join(ks, " | "), strings.Repeat(" --- |", len(ks)+2))
		for _, r := range byFam[f] {
			fmt.Fprintf(&sb, "| %s | %s |", r.Model, r.Condition)
			for _, k := range ks {
				v, ok := r.Metrics[k]
				if !ok {
					v = math.NaN()
				}
				fmt.Fprintf(&sb, " %s |", f2(v))
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
