package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueChildProgressCLIReadPathsAndPagination(t *testing.T) {
	f := newFakeServer(t)
	children := &protocol.IssueChildProgress{Total: 3, Done: 1, Running: 1, Cancelled: 1, Items: []protocol.IssueChildSummary{{IssueID: "child-active", Title: "Investigate clipboard", Status: protocol.IssueInProgress, Execution: protocol.ExecutionCounts{Running: 1}}}}
	summary := protocol.IssueSummary{IssueControl: protocol.IssueControl{IssueID: "parent", State: protocol.IssueActive, WorkStatus: protocol.IssueInProgress}, Title: "Clipboard support", Board: &protocol.IssueBoardCard{Status: protocol.IssueInProgress, Children: children}}
	f.answer("GET /api/v1/issues", protocol.IssuePage{Items: []protocol.IssueSummary{summary}})
	f.answer("GET /api/v1/issues/parent", protocol.IssueView{Issue: protocol.Issue{IssueControl: summary.IssueControl, Plan: protocol.IssuePlan{Title: summary.Title}}, Children: children})
	for _, args := range [][]string{{"issue", "list", "--board"}, {"issue", "show", "parent"}} {
		e := newTestEnv(t, f.srv.URL)
		if code := e.main(args...); code != exitOK {
			t.Fatal(args, code, e.errOut.String())
		}
		for _, want := range []string{"1/3 done", "1 cancelled", "1 executing", "child-active", "Investigate clipboard", "--parent parent", "--limit / --after"} {
			if !strings.Contains(e.out.String(), want) {
				t.Fatal("missing child evidence", want, e.out.String())
			}
		}
	}
	f.on("GET /api/v1/issues", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		query := r.URL.Query()
		if query.Get("parent_id") != "parent" || query.Get("include_closed") != "true" || query.Get("after") != "child-cursor" || query.Get("limit") != "2" {
			t.Error("child pagination scope lost", query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"more":false}`))
	})
	e := newTestEnv(t, f.srv.URL)
	if e.main("issue", "list", "--parent", "parent", "--include-closed", "--after", "child-cursor", "--limit", "2", "--json") != exitOK {
		t.Fatal(e.errOut.String())
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("reading child progress wrote business state")
	}
}
