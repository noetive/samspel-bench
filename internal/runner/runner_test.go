package runner

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noetive/samspel-bench/internal/task"
)

// End to end with scripted mock agents: communication must beat no
// communication, match the oracle on core, and ablation must fall back.
func TestMockEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Models: []string{"mock"}, Seed: 3,
		Families:   []FamilySpec{{Name: "F1", Instances: 20}, {Name: "F5", N: 3, Instances: 10, Params: task.Params{"requirements": 20.0}}, {Name: "F8", Instances: 10}},
		Conditions: []Condition{{Name: "core"}, {Name: "byz", Byzantine: Byzantine{Count: 1, Script: "confident_wrong"}}},
		Controls:   []string{"team", "nocomm", "oracle", "ablation"},
		Agent:      AgentSpec{WaitTimeoutSec: 2, RunTimeoutSec: 30},
	}
	cfg.defaults()
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	r := New(cfg, Options{Out: dir, Parallel: 32, Mock: true, MockLatency: time.Millisecond, Log: slog.New(slog.DiscardHandler)})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "results.jsonl")); err != nil {
		t.Fatal(err)
	}
	// Rerun must resume with nothing left to do.
	before := countLines(t, filepath.Join(dir, "results.jsonl"))
	if err := New(cfg, Options{Out: dir, Parallel: 4, Mock: true, Log: slog.New(slog.DiscardHandler)}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := countLines(t, filepath.Join(dir, "results.jsonl")); after != before {
		t.Fatalf("resume reran jobs: %d -> %d", before, after)
	}
	s := map[string]map[string][2]int{} // family|cond -> control -> ok,total
	for _, j := range Expand(cfg) {
		_ = j
	}
	res := readResults(t, filepath.Join(dir, "results.jsonl"))
	for _, x := range res {
		if x.Error != "" {
			t.Fatalf("run error: %s", x.Error)
		}
		k := x.Family + "|" + x.Condition
		if s[k] == nil {
			s[k] = map[string][2]int{}
		}
		v := s[k][x.Control]
		v[1]++
		if x.Success {
			v[0]++
		}
		s[k][x.Control] = v
	}
	rate := func(k, c string) float64 { v := s[k][c]; return float64(v[0]) / float64(v[1]) }
	for _, f := range []string{"F1", "F5", "F8"} {
		k := f + "|core"
		if rate(k, "team") < 0.99 || rate(k, "oracle") < 0.99 {
			t.Errorf("%s: team %.2f oracle %.2f, want 1", f, rate(k, "team"), rate(k, "oracle"))
		}
		if rate(k, "nocomm") > 0.5 || rate(k, "ablation") > 0.5 {
			t.Errorf("%s: nocomm %.2f ablation %.2f should be low", f, rate(k, "nocomm"), rate(k, "ablation"))
		}
		if rate(f+"|byz", "team") >= rate(k, "team") {
			t.Errorf("%s: a confident-wrong peer should hurt the credulous mock team", f)
		}
	}
}

// A run carried over the spool must behave like one carried in memory: the
// mock team still succeeds on core, and every communicating run leaves its
// message file behind for inspection.
func TestMockRunOverSpool(t *testing.T) {
	dir, spool := t.TempDir(), t.TempDir()
	cfg := &Config{
		Models: []string{"mock"}, Seed: 5,
		Families:   []FamilySpec{{Name: "F1", Instances: 4}, {Name: "F8", Instances: 4}},
		Conditions: []Condition{{Name: "core"}},
		Controls:   []string{"team", "nocomm"},
		Agent:      AgentSpec{WaitTimeoutSec: 2, RunTimeoutSec: 30},
	}
	cfg.defaults()
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	opts := Options{Out: dir, Parallel: 8, Mock: true, MockLatency: time.Millisecond, SpoolDir: spool, Log: slog.New(slog.DiscardHandler)}
	if err := New(cfg, opts).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	teams := 0
	for _, x := range readResults(t, filepath.Join(dir, "results.jsonl")) {
		if x.Error != "" {
			t.Fatalf("%s: %s", x.JobID, x.Error)
		}
		if x.Control != "team" {
			continue
		}
		teams++
		if !x.Success {
			t.Errorf("%s %s team failed over the spool", x.Family, x.JobID)
		}
		b, err := os.ReadFile(filepath.Join(spool, x.JobID+".jsonl"))
		if err != nil || len(b) == 0 {
			t.Errorf("%s: no messages in spool file (err %v)", x.JobID, err)
		}
	}
	if teams != 8 {
		t.Fatalf("%d team runs, want 8", teams)
	}
}
