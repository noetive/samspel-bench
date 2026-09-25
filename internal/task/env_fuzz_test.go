package task

import (
	"fmt"
	"testing"

	"github.com/noetive/samspel-bench/internal/trace"
)

// Tool inputs are written by the model under test, so every family's
// environment must survive any bytes on any tool name: a panic would abort the
// whole benchmark over one malformed call, and a verifier that trips on the
// state a bad call left behind would lose the run's score.
func FuzzEnvCall(f *testing.F) {
	for _, in := range []string{`{}`, `{"answer":"x"}`, `{"to":"agent-2","amount":-1,"idempotency_key":""}`, `{"answers":[{"id":"Q-01","value":1}]}`, `null`, `[`, `"`} {
		f.Add(uint64(1), "", []byte(in))
	}
	f.Add(uint64(2), "no_such_tool", []byte(`{"amount":1e400}`))
	f.Fuzz(func(t *testing.T, seed uint64, tool string, input []byte) {
		for _, name := range Names() {
			fam := Registry[name]
			ids := make([]string, max(fam.MinAgents(), 4))
			for i := range ids {
				ids[i] = fmt.Sprintf("agent-%d", i+1)
			}
			inst, err := fam.Generate(seed, Params{}, ids)
			if err != nil {
				t.Fatalf("%s: generate: %v", name, err)
			}
			for _, oracle := range []bool{false, true} {
				env := inst.NewEnv(EnvConfig{Decision: "all", LostReply: 0.5, Oracle: oracle, Seed: seed})
				agent := ids[int(seed%uint64(len(ids)))]
				names := []string{tool}
				for _, tl := range env.Tools(agent) {
					names = append(names, tl.Name)
				}
				for _, n := range names {
					if res, _ := env.Call(agent, n, input); res == "" {
						t.Errorf("%s: %s returned an empty result; the agent would read that as nothing happened", name, n)
					}
				}
				_ = env.Done()
				_ = inst.Verify(env, ids, trace.New())
			}
		}
	})
}
