package views

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUsageSessionKeepsAttributionsAndMeasurementsDistinct(t *testing.T) {
	g := newUsageGrouping()
	a := UsageAggregate{Record: protocol.TokenUsageRecord{TokenUsageSample: protocol.TokenUsageSample{SessionID: "session", Model: "model", Provider: "codex", NativeVersion: "v1"}, Attribution: protocol.UsageAttribution{RunID: "run-one", IssuePath: []protocol.UsageIssueRef{{ID: "parent"}, {ID: "child"}}}}, Totals: protocol.TokenUsageTotals{Requests: 1, Total: 10, TotalKnown: 1}}
	g.addAggregate(a)
	g.addAggregate(a)
	a.Record.NativeVersion = "v2"
	g.addAggregate(a)
	a.Record.Attribution.RunID = "run-two"
	g.addAggregate(a)
	a.Record.Attribution.RunID = "run-one"
	a.Record.Attribution.Categories = []string{}
	g.addAggregate(a)
	var view protocol.TokenUsageView
	g.finish(&view)
	if len(view.Sessions) != 1 {
		t.Fatal(view.Sessions)
	}
	s := view.Sessions[0]
	if s.Totals.Requests != 5 || s.Totals.Total != 50 || len(s.Executions) != 3 || len(s.Attributions) != 3 {
		t.Fatal("request, execution or nil/empty attribution identity was merged", s)
	}
	if s.Executions[0].Totals.Total != 30 || len(s.Executions[0].Measurements) != 2 || s.Executions[0].Measurements[0].Totals.Total != 20 || s.Executions[0].Measurements[1].Totals.Total != 10 {
		t.Fatal("CLI versions or repeated contributions were merged incorrectly", s.Executions)
	}
	for _, issue := range view.Issues {
		if issue.Totals.Total != 50 {
			t.Fatal("ancestor rollup changed", issue)
		}
		if issue.ID == "child" && (issue.Direct.Total != 50 || len(issue.Sessions) != 1 || len(issue.Sessions[0].Executions) != 3) {
			t.Fatal("direct issue lost execution measurements", issue)
		}
		if issue.ID == "parent" && (issue.Direct.Total != 0 || len(issue.Sessions) != 0) {
			t.Fatal("ancestor became direct attribution", issue)
		}
	}
}
