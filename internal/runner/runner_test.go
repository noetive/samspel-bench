package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noetive/samspel-bench/internal/llm"
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

// failingModel fails every request with a fixed error.
type failingModel struct {
	err   error
	calls atomic.Int32
}

func (m *failingModel) Complete(context.Context, llm.Request) (*llm.Response, error) {
	m.calls.Add(1)
	return nil, m.err
}

// A flaky network must not decide a benchmark: a run that fails only through
// transient API failures is rerun, and recorded as an error rather than a
// score if it never gets through. A run the API refuses outright is not
// rerun, because every rerun spends tokens for the same refusal.
func TestRunRetriesOnlyTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		wantAttempts int
	}{
		{"transient", fmt.Errorf("%w: connection reset", llm.ErrTransient), 3},
		{"lasting", errors.New("status 400: bad request"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &Config{
				Models: []string{"m"}, Seed: 1,
				Families:   []FamilySpec{{Name: "F1", Instances: 1}},
				Conditions: []Condition{{Name: "core"}},
				Controls:   []string{"team"},
				Agent:      AgentSpec{WaitTimeoutSec: 1, RunTimeoutSec: 5},
			}
			cfg.defaults()
			if err := cfg.validate(); err != nil {
				t.Fatal(err)
			}
			model := &failingModel{err: tc.err}
			opts := Options{Out: dir, Parallel: 1, Model: model, RunRetries: 2, RetryBackoff: time.Millisecond, Log: slog.New(slog.DiscardHandler)}
			if err := New(cfg, opts).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			res := readResults(t, filepath.Join(dir, "results.jsonl"))
			if len(res) != 1 {
				t.Fatalf("%d results, want 1", len(res))
			}
			got := res[0]
			if got.Attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", got.Attempts, tc.wantAttempts)
			}
			if got.Error == "" || got.Success {
				t.Errorf("a run whose every call failed must be an error, got %+v", got)
			}
			if got.Transient != errors.Is(tc.err, llm.ErrTransient) {
				t.Errorf("transient = %v for %v", got.Transient, tc.err)
			}
		})
	}
}
