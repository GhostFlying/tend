package cli

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"strings"
	"testing"
)

func TestIssueCLISeparatesTerminalStatusAndHistoricalAssessment(t *testing.T) {
	f := newFakeServer(t)
	control := protocol.IssueControl{IssueID: "completed", State: protocol.IssueClosed, WorkStatus: protocol.IssueInProgress}
	progress := &protocol.IssueProgress{Summary: "Installation not started", NextStep: "Install release", IssueRevision: 73, Historical: true, HistoricalReason: "issue_ended"}
	f.answer("GET /api/v1/issues/completed", protocol.IssueView{EffectiveWorkStatus: protocol.IssueDone, Issue: protocol.Issue{IssueControl: control, Revision: 81, Plan: protocol.IssuePlan{Title: "Completed delivery"}}, Progress: progress})
	e := newTestEnv(t, f.srv.URL)
	if code := e.main("issue", "show", "completed"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	text := e.out.String()
	for _, want := range []string{"closed / done", "Historical assessment (revision 73; issue_ended)", "Historical next step: Install release"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "Current result: Installation not started") {
		t.Fatal("old assessment presented as current", text)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("read rewrote terminal business")
	}
}
