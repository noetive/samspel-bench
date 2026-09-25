package report

import (
	"math"
	"testing"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
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
