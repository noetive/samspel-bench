package report

import (
	"fmt"
	"math"
	"testing"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/noetive/samspel-bench/internal/runner"
)

// report.json is the machine-readable scorecard. A row whose gain is undefined
// (no paired nocomm and oracle runs yet) must still encode, with the undefined
// statistics as null, or a partial benchmark produces no scorecard at all.
func TestRowJSON_UndefinedStatisticsEncodeAsNull(t *testing.T) {
	nan := math.NaN()
	row := Row{Model: "m", S: map[string]float64{"team": 0.5}, G: nan, GCI: [2]float64{nan, nan}, STeamCI: [2]float64{0.25, 0.75}}
	b, err := json.Marshal([]Row{row})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := gjson.ParseBytes(b).Get("0")
	if g := got.Get("gain"); g.Type != gjson.Null {
		t.Errorf("gain = %s, want null", g.Raw)
	}
	if ci := got.Get("gain_ci").Raw; ci != "[null,null]" {
		t.Errorf("gain_ci = %s, want [null,null]", ci)
	}
	if lo, hi := got.Get("success_team_ci.0").Float(), got.Get("success_team_ci.1").Float(); lo != 0.25 || hi != 0.75 {
		t.Errorf("success_team_ci = %s, want [0.25,0.75]", got.Get("success_team_ci").Raw)
	}
	if s := got.Get("success.team").Float(); s != 0.5 {
		t.Errorf("success.team = %v, want 0.5", s)
	}
	if n := len(got.Map()); n != 12 {
		t.Errorf("row has %d fields, want 12 (no duplicated keys from the override): %s", n, got.Raw)
	}
}

// A scorecard is read by people deciding which model collaborates better. A
// handful of instances that all succeeded must not be shown with an interval
// of zero width, which reads as certainty; enough instances must get one.
func TestBuild_IntervalsOnlyWithEnoughInstances(t *testing.T) {
	results := func(n int) []runner.Result {
		var rs []runner.Result
		for i := 0; i < n; i++ {
			for _, c := range []string{"team", "nocomm", "oracle"} {
				rs = append(rs, runner.Result{JobID: fmt.Sprintf("%d-%s", i, c), Model: "m", Family: "F1", Condition: "core", Control: c, Seed: uint64(i), Success: c != "nocomm" || i%2 == 0})
			}
		}
		return rs
	}
	few := Build(results(minCIInstances-1), 200)[0]
	if !math.IsNaN(few.STeamCI[0]) || !math.IsNaN(few.GCI[0]) {
		t.Errorf("%d instances got intervals %v %v", minCIInstances-1, few.STeamCI, few.GCI)
	}
	enough := Build(results(minCIInstances+10), 200)[0]
	if math.IsNaN(enough.STeamCI[0]) || math.IsNaN(enough.GCI[0]) {
		t.Errorf("%d instances got no interval", minCIInstances+10)
	}
}
