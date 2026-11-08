package cli

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueResponsibilityCLIShowsNextStepCurrentRouteAndPagination(t *testing.T) {
	f := newFakeServer(t)
	control := protocol.IssueControl{IssueID: "parent", State: protocol.IssueActive, WorkStatus: protocol.IssueInProgress}
	progress := &protocol.IssueProgress{NextStep: "Settle the accepted deployment", IssueRevision: 7}
	f.answer("GET /api/v1/issues", protocol.IssuePage{Items: []protocol.IssueSummary{{IssueControl: control, Title: "Delivery", Board: &protocol.IssueBoardCard{Status: protocol.IssueInProgress, Progress: progress, AttentionCount: 51, OwnerAttentionCount: 2, Execution: protocol.ExecutionCounts{Completed: 4}}}}})
	f.answer("GET /api/v1/issues/parent", protocol.IssueView{Issue: protocol.Issue{IssueControl: control, Plan: protocol.IssuePlan{Title: "Delivery"}}, Progress: progress, Attentions: &protocol.AttentionPage{More: true, Next: "attention-cursor", Items: []protocol.AttentionView{{Attention: protocol.Attention{AttentionID: "original-question", IssueID: "child", Title: "Check deployment settlement", Responsibility: &protocol.AttentionResponsibility{IssueID: "parent", RecipientKind: "agent"}}}}}})
	for _, tc := range []struct {
		args  []string
		wants []string
	}{
		{[]string{"issue", "list", "--board"}, []string{"active / in_progress", "51 open actions", "2 need the human owner", "Next step (assessment at revision 7): Settle the accepted deployment"}},
		{[]string{"issue", "show", "parent"}, []string{"Current responsibility actions:", "original-question", "agent; origin: child", "--responsible-issue parent --recipient all --status open --actionable-only --all --after attention-cursor"}},
	} {
		e := newTestEnv(t, f.srv.URL)
		if code := e.main(tc.args...); code != exitOK {
			t.Fatal(code, e.errOut.String())
		}
		for _, want := range tc.wants {
			if !strings.Contains(e.out.String(), want) {
				t.Fatalf("missing %q in %s", want, e.out.String())
			}
		}
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("reading current responsibility wrote business state")
	}
}
