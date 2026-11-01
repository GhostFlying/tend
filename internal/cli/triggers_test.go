package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueDefaultSummaryAndJSONAreDistinct(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	f.answer("GET /api/v1/issues", protocol.IssuePage{Items: []protocol.IssueSummary{{Title: "Inspect current facts", IssueControl: protocol.IssueControl{IssueID: "wi_example", State: protocol.IssueActive}}}})
	if code := te.main("issue"); code != 0 || !strings.Contains(te.out.String(), "Inspect current facts") || strings.HasPrefix(te.out.String(), "{") {
		t.Fatal(code, te.out.String(), te.errOut.String())
	}
	te.out.Reset()
	if code := te.main("issue", "list", "--json"); code != 0 {
		t.Fatal(code)
	}
	var page protocol.IssuePage
	if err := json.Unmarshal(te.out.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatal(err, page)
	}
}
func TestScheduleCLIPreservesCanonicalIssueRevisionAndEpoch(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	f.answer("GET /api/v1/issues/wi_target", protocol.IssueView{Issue: protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "wi_target", AssignmentEpoch: 7}, Revision: 9}})
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, r *http.Request, b []byte) {
		var env protocol.Envelope
		if json.Unmarshal(b, &env) != nil || env.Operation != "issues.followups_create" || env.Target.Type != "issue" || *env.ExpectedControlRevision != 9 {
			t.Error(env)
		}
		var p protocol.IssueFollowupPayload
		_ = json.Unmarshal(env.Payload, &p)
		if p.ExpectedAssignmentEpoch != 7 || p.Spec == nil || p.Spec.Cron != "0 9 * * 1-5" {
			t.Error(p)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	})
	if code := te.main("schedule", "create", "--issue", "wi_target", "--data", `{"kind":"cron","cron":"0 9 * * 1-5","timezone":"UTC","purpose":"Check","instruction":"Inspect","expires_at":9999999999999}`); code != 0 {
		t.Fatal(code, te.errOut.String())
	}
}
func TestWebhookDryRunNeverStagesPrivateKey(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	f.answer("GET /api/v1/issues/wi_target", protocol.IssueView{Issue: protocol.Issue{IssueControl: protocol.IssueControl{AssignmentEpoch: 1}, Revision: 2}})
	if code := te.main("webhook", "create", "--issue", "wi_target", "--data", `{"title":"Check","instruction":"Read current facts"}`, "--secret-file", "does-not-exist", "--dry-run"); code != 0 {
		t.Fatal(code, te.errOut.String())
	}
	var env protocol.Envelope
	if err := json.Unmarshal(te.out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Operation != "webhooks.create" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 2 {
		t.Fatal(env)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("dry-run mutated")
	}
}

func TestScheduleHistoryForwardsIndependentCursors(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	f.answer("GET /api/v1/diagnostics/schedules/rule", protocol.TriggerSchedule{ID: "rule", IssueID: "wi_target", Source: "followup"})
	f.on("GET /api/v1/issues/wi_target/followups/rule/history", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		q := r.URL.Query()
		if q.Get("after_revision") != "51" || q.Get("after_occurrence") != "occ_previous" {
			t.Error("history continuation reset", q)
		}
		_ = json.NewEncoder(w).Encode(protocol.IssueFollowupHistory{})
	})
	if code := te.main("schedule", "history", "rule", "--after-revision", "51", "--after-occurrence", "occ_previous"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
}
