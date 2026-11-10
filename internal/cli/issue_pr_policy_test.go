package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssuePRPolicyFreezesSelectedPlanWithoutImplicitTaskAssessment(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	issue := protocol.IssueView{Issue: protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "issue", AssignmentEpoch: 3}, Revision: 7,
		Plan: protocol.IssuePlan{Title: "Existing work", Project: "default", Intent: "Preserve original scope", Stages: []protocol.IssueStage{}}}}
	f.answer("GET /api/v1/issues/issue", issue)
	if code := e.main("issue", "pr-policy", "set", "issue", "--mode", "auto", "--repository", "example/project", "--reason", "Integrate after required checks", "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var original protocol.Envelope
	if err := json.Unmarshal(e.out.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	var payload protocol.UpdateIssuePayload
	if err := json.Unmarshal(original.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if original.Operation != "issues.update" || original.Target.ID != issue.IssueID || original.ExpectedControlRevision == nil || *original.ExpectedControlRevision != 7 || payload.ExpectedAssignmentEpoch != 3 || payload.Plan.Intent != issue.Plan.Intent || len(payload.TaskDispositions) != 0 || payload.Plan.PRPolicy == nil || payload.Plan.PRPolicy.Mode != "auto" || payload.Plan.PRPolicy.Authority != nil {
		t.Fatal("policy helper changed scope, invented a source, or assessed a Task", original, payload)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("dry-run wrote policy")
	}
	file := filepath.Join(t.TempDir(), "original.json")
	if err := os.WriteFile(file, e.out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var got protocol.Envelope
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		wantHash, wantErr := protocol.PayloadHash(&original)
		gotHash, gotErr := protocol.PayloadHash(&got)
		if wantErr != nil || gotErr != nil || wantHash != gotHash || got.RequestID != original.RequestID || got.Target != original.Target || got.Operation != original.Operation || got.ExpectedControlRevision == nil || *got.ExpectedControlRevision != *original.ExpectedControlRevision {
			t.Error("recovery rebuilt the policy request", got, wantErr, gotErr)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: original.RequestID, Status: protocol.OpSucceeded}})
	})
	reads := len(f.requests)
	e.out.Reset()
	if code := e.main("issue", "replay", "--file", file, "--json"); code != exitOK || len(f.requests) != reads+1 {
		t.Fatal("recovery fetched a newer plan", code, e.errOut.String())
	}
}

func TestIssuePRPolicyRejectsUnclearGrantBeforeIO(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	for _, args := range [][]string{
		{"set", "issue", "--mode", "automatic", "--repository", "example/project", "--reason", "Grant"},
		{"set", "issue", "--mode", "auto", "--reason", "Grant"},
		{"set", "issue", "--mode", "auto", "--repository", "example/project"},
		{"clear", "issue"},
		{"show", "issue", "--source-message", "source"},
		{"set", "issue", "--mode", "auto", "--repository", "example/project", "--reason", "Grant", "--deploy-hosts", "runner-one"},
		{"set", "issue", "--mode", "manual", "--repository", "example/project", "--reason", "Grant", "--deploy-server-instance", "service"},
	} {
		if code := e.main(append([]string{"issue", "pr-policy"}, args...)...); code != exitUsage {
			t.Fatal(args, code, e.errOut.String())
		}
	}
	if len(f.requests) != 0 {
		t.Fatal("invalid policy performed network IO")
	}
}

func TestIssuePRPolicyFreezesExplicitDeploymentTargets(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	issue := protocol.IssueView{Issue: protocol.Issue{IssueControl: protocol.IssueControl{IssueID: "issue", AssignmentEpoch: 3}, Revision: 7, Plan: protocol.IssuePlan{Title: "Same work", Intent: "Deliver this existing scope"}}}
	f.answer("GET /api/v1/issues/issue", issue)
	if code := e.main("issue", "pr-policy", "set", "issue", "--mode", "auto", "--repository", "example/project", "--reason", "Deploy this Issue within its existing target", "--deploy-server-instance", "service", "--deploy-hosts", "runner-one,runner-two", "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var envelope protocol.Envelope
	var payload protocol.UpdateIssuePayload
	if err := json.Unmarshal(e.out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := &protocol.IssueDeploymentScope{ServerInstance: "service", HostIDs: []string{"runner-one", "runner-two"}}
	if payload.Plan.PRPolicy == nil || !reflect.DeepEqual(payload.Plan.PRPolicy.Deployment, want) || payload.Plan.PRPolicy.Authority != nil {
		t.Fatal("scope or real owner provenance was lost", payload)
	}
}

func TestIssueSharedReviewReadPreservesCursorAndResult(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	want := protocol.SharedDeliveryReviewPage{Items: []protocol.SharedDeliveryReview{{ReviewID: "review", Status: "passed", Valid: true, Links: []protocol.SharedDeliveryReviewLink{{IssueID: "issue", DeliveryID: "github:example/project#12"}}, Rounds: []protocol.SharedDeliveryReviewRound{}}}, More: true, Next: "next"}
	f.on("GET /api/v1/issues/issue/delivery-reviews", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		q := r.URL.Query()
		if q.Get("delivery_id") != "github:example/project#12" || q.Get("after") != "previous" || q.Get("limit") != "2" {
			t.Error("review filters were lost", q)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	if code := e.main("issue", "delivery-reviews", "issue", "--delivery", "github:example/project#12", "--after", "previous", "--limit", "2", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var got protocol.SharedDeliveryReviewPage
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("review provenance or pagination was lost", got, err)
	}
}

func TestIssueSharedReviewCommandPreservesActualEvidence(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	input := `{"expected_assignment_epoch":3,"delivery_id":"github:example/project#12","action":"start","reason":"Import the existing whole-PR reviewer result","reviewer_task_id":"task-code","acceptance_id":"acc-real","reviewer_role":"accept"}`
	if code := e.main("issue", "review-delivery", "issue", "--revision", "7", "--epoch", "3", "--data", input, "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(e.out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var got, want protocol.UpdateSharedDeliveryReviewPayload
	if err := json.Unmarshal(envelope.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	if envelope.Operation != "issues.review_delivery" || envelope.Target.ID != "issue" || envelope.ExpectedControlRevision == nil || *envelope.ExpectedControlRevision != 7 || !reflect.DeepEqual(got, want) || len(f.requests) != 0 {
		t.Fatal("CLI dropped review evidence or performed implicit IO", envelope, got)
	}
}

func TestDoctorServerDeliveryObserverConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		observer *protocol.DeliveryObserverStatus
		status   string
		failed   bool
	}{
		{"legacy", nil, "", false},
		{"configured", &protocol.DeliveryObserverStatus{Enabled: true, Providers: []string{"github"}, Executables: map[string]bool{"github": true}, TimeoutMillis: 30000}, "ok", false},
		{"missing executable", &protocol.DeliveryObserverStatus{Enabled: true, Providers: []string{"github"}, Executables: map[string]bool{"github": false}}, "unavailable", true},
		{"empty providers", &protocol.DeliveryObserverStatus{Enabled: true, Providers: []string{}, Executables: map[string]bool{}}, "abnormal", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			f.answer("GET /api/v1/deployment", map[string]string{"deployment_id": "dep-test"})
			f.answer("GET /api/v1/session", protocol.SessionView{})
			f.answer("GET /api/v1/snapshot", protocol.Snapshot{DeliveryObserver: tc.observer})
			f.answer("GET /api/v1/coordination/metrics", protocol.CoordinationMetrics{})
			f.answer("GET /api/v1/conversation/settings", protocol.ConversationSettingsView{})
			code := e.main("doctor", "--json")
			if (code != exitOK) != tc.failed {
				t.Fatal(code, e.errOut.String())
			}
			var checks []diagnosticCheck
			if err := json.Unmarshal(e.out.Bytes(), &checks); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, check := range checks {
				if check.Name == "delivery-observer" {
					found = true
					if check.Status != tc.status {
						t.Fatal(check)
					}
				}
			}
			if found != (tc.observer != nil) {
				t.Fatal("legacy server acquired invented observer status", checks)
			}
		})
	}
}

func TestIssueReviewRecoveryPreservesBothControlRevisions(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	input := `{"expected_assignment_epoch":3,"expected_review_revision":11,"delivery_id":"github:example/project#12","action":"authorize_recheck","reason":"Authorize one focused round after escalation"}`
	if code := e.main("issue", "review-delivery", "issue", "--revision", "7", "--epoch", "3", "--data", input, "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(e.out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var payload protocol.UpdateSharedDeliveryReviewPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if envelope.ExpectedControlRevision == nil || *envelope.ExpectedControlRevision != 7 || payload.ExpectedAssignmentEpoch != 3 || payload.ExpectedReviewRevision == nil || *payload.ExpectedReviewRevision != 11 || payload.Action != "authorize_recheck" || len(f.requests) != 0 {
		t.Fatal("explicit review recovery lost a CAS or read a new version", envelope, payload)
	}
}
