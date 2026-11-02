package views

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
)

func TestUsageViewsConserveSessionAndIssueContributions(t *testing.T) {
	g := newUsageGrouping()
	for _, id := range []string{"leaf-a", "leaf-b"} {
		n := int64(100)
		g.add(protocol.TokenUsageRecord{Estimate: &protocol.UsageEstimate{USD: "0.1"}, TokenUsageSample: protocol.TokenUsageSample{SessionID: "shared-session", Total: &n, Settlement: "final"}, Attribution: protocol.UsageAttribution{IssuePath: []protocol.UsageIssueRef{{ID: "parent", Title: "Parent"}, {ID: id, Title: id}}, TaskID: id}})
	}
	n := int64(50)
	g.add(protocol.TokenUsageRecord{Estimate: &protocol.UsageEstimate{USD: "0.1"}, TokenUsageSample: protocol.TokenUsageSample{SessionID: "main", Total: &n, Settlement: "final"}})
	v := protocol.TokenUsageView{}
	g.finish(&v)
	if len(v.Sessions) != 2 {
		t.Fatal(v.Sessions)
	}
	var sessionTotal, rootTotal int64
	for _, s := range v.Sessions {
		sessionTotal += s.Totals.Total
	}
	for _, i := range v.Issues {
		if i.ParentID == "" {
			rootTotal += i.Totals.Total
		}
		if i.ID == "parent" && (i.Direct.Total != 0 || i.Totals.Total != 200 || len(i.Sessions) != 0) {
			t.Fatal(i)
		}
		if i.ParentID == "parent" && (i.Direct.Total != 100 || len(i.Sessions) != 1 || i.Sessions[0].Totals.Total != 100) {
			t.Fatal(i)
		}
	}
	for _, session := range v.Sessions {
		if session.Totals.EstimatedRequests != session.Totals.Requests {
			t.Fatal(session)
		}
	}
	if sessionTotal != 250 || rootTotal != 250 {
		t.Fatal(sessionTotal, rootTotal)
	}
}

func TestIssueRecentOrderUsesLatestDescendantRecordTime(t *testing.T) {
	g := newUsageGrouping()
	n, earlier, latest := int64(100), int64(1000), int64(2000)
	g.add(protocol.TokenUsageRecord{TokenUsageSample: protocol.TokenUsageSample{Total: &n, OccurredAt: &earlier}, Attribution: protocol.UsageAttribution{IssuePath: []protocol.UsageIssueRef{{ID: "a", Title: "Earlier"}}}})
	g.add(protocol.TokenUsageRecord{TokenUsageSample: protocol.TokenUsageSample{Total: &n, OccurredAt: &latest}, Attribution: protocol.UsageAttribution{IssuePath: []protocol.UsageIssueRef{{ID: "z", Title: "Recent parent"}, {ID: "leaf", Title: "Recent leaf"}}}})
	g.add(protocol.TokenUsageRecord{TokenUsageSample: protocol.TokenUsageSample{Total: &n}, Attribution: protocol.UsageAttribution{IssuePath: []protocol.UsageIssueRef{{ID: "unknown", Title: "Unknown time"}}}})
	var v protocol.TokenUsageView
	g.finish(&v)
	var roots []string
	for _, issue := range v.Issues {
		if issue.ParentID == "" {
			roots = append(roots, issue.ID)
		}
	}
	if len(roots) != 3 || roots[0] != "z" || roots[1] != "a" || roots[2] != "unknown" {
		t.Fatal(roots)
	}
}
