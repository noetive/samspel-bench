package task

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

var ids4 = []string{"agent-1", "agent-2", "agent-3", "agent-4"}

// Every F1 instance must need all three key clues: the reference solver
// with any single agent's view must not be guaranteed right, with all views
// it must be right.
func TestF1Invariants(t *testing.T) {
	for seed := uint64(0); seed < 300; seed++ {
		inst, err := f1{}.Generate(seed, Params{}, ids4)
		if err != nil {
			t.Fatal(err)
		}
		in := inst.(*f1Inst)
		want := in.sus[in.culprit].name
		if got := in.solve(in.Briefing() + in.OracleView()); got != want {
			t.Fatalf("seed %d: oracle solve %s, want %s", seed, got, want)
		}
		keys := 0
		for _, id := range ids4 {
			n := strings.Count(in.View(id), "person responsible")
			if n > 1 {
				t.Fatalf("seed %d: %s holds %d key clues", seed, id, n)
			}
			keys += n
		}
		if keys != 3 {
			t.Fatalf("seed %d: %d key clues", seed, keys)
		}
		if in.falseTarget != "" {
			// The false clue plus the two true clues it doesn't replace must
			// point at the planted innocent.
			text := in.Briefing() + in.falseClaims[0]
			for _, a := range in.keyAttrs {
				if a != in.falseAttr {
					text += "\n" + f1Clue(a, in.sus[in.culprit].attrs[a])
				}
			}
			if got := in.solve(text); got != in.falseTarget {
				t.Fatalf("seed %d: false claim leads to %s, want %s", seed, got, in.falseTarget)
			}
		}
	}
}

func TestF8IdempotencyAndOverdraft(t *testing.T) {
	inst, _ := f8{}.Generate(3, Params{"transfers": 6}, ids4)
	in := inst.(*f8Inst)
	env := in.NewEnv(EnvConfig{Seed: 1}).(*f8Env)
	tr := in.transfers[0]
	call := func(key string) string {
		b, _ := json.Marshal(map[string]any{"to": tr.To, "amount": tr.Amount, "idempotency_key": key})
		r, _ := env.Call(tr.From, "transfer", b)
		return r
	}
	if r := call(tr.ID); !strings.HasPrefix(r, "applied") {
		t.Fatal(r)
	}
	if r := call(tr.ID); !strings.HasPrefix(r, "already applied") {
		t.Fatal("same key must not apply twice: " + r)
	}
	b, _ := json.Marshal(map[string]any{"to": tr.To, "amount": 1 << 20})
	if r, _ := env.Call(tr.From, "transfer", b); !strings.HasPrefix(r, "rejected") {
		t.Fatal("overdraft must be rejected: " + r)
	}
	// Oracle executing the list in order must succeed.
	o := in.NewEnv(EnvConfig{Oracle: true}).(*f8Env)
	for _, t2 := range in.transfers {
		b, _ := json.Marshal(map[string]any{"from": t2.From, "to": t2.To, "amount": t2.Amount, "idempotency_key": t2.ID})
		if r, _ := o.Call(OracleID, "transfer", b); !strings.HasPrefix(r, "applied") {
			t.Fatal(r)
		}
	}
	if out := in.Verify(o, nil, nil); !out.Success || out.Metrics["exactly_once_rate"] != 1 {
		t.Fatalf("oracle should pass: %+v", out)
	}
}

func TestMarkerWindow(t *testing.T) {
	m := Marker{Phrases: []string{"export job", "retry", "5"}, Window: 40}
	if !m.In("export job: retry 5, timeout 40") {
		t.Fatal("should match")
	}
	if m.In("export job: retry 15") {
		t.Fatal("5 inside 15 must not match")
	}
	if m.In("export job retry 7 ................................................................ batch 5") {
		t.Fatal("value outside the window must not match")
	}
}
