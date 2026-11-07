package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestObservationReaderRunCredentialScopeAndCancellation(t *testing.T) {
	calls := 0
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		calls++
		if q.Method != "GET" || q.URL.Path != "/runner/v1/observation" || q.URL.RawQuery != "" || q.Header.Get(headerRun) != "run_one" || q.Header.Get("Authorization") != "Bearer run-secret" {
			t.Error("unexpected request identity")
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte("run-secret"))
			return
		}
		pending := int64(4)
		_ = json.NewEncoder(w).Encode(protocol.RunObservation{Version: 1, RunID: "run_one", Seq: int64(calls), SharedReviews: []protocol.ObservedDeliveryReview{{ReviewID: "accept-review", Provider: "github", Repository: "example/repo", Number: 7, Status: "reviewing", RoundLimit: 2, RoundsUsed: 1, Round: &protocol.ObservedDeliveryReviewRound{TaskID: "task_one", ReviewerRole: protocol.RoleAccept, ReviewerProfile: &protocol.ExecutionProfile{Model: "actual-model", Effort: "xhigh"}, ArtifactID: "actual-artifact"}}}, DeliveryAuthorization: &protocol.DeliveryExecutionAuthority{TaskID: "task_one", SourceTaskID: "task_source", RunID: "run_one", LeaseID: "lease_one", DutyRevision: 2, Candidate: protocol.IssueDeliveryCandidate{HeadSHA: "original-head"}, Valid: false, InvalidReason: "authorization_not_current", Provider: &protocol.ObservedDeliveryProvider{CI: &protocol.DeliveryCI{HeadSHA: "original-head", State: "failure"}, Mergeability: "unknown"}, SharedReview: &protocol.ObservedDeliveryReview{ReviewID: "review_original", Valid: false, Round: &protocol.ObservedDeliveryReviewRound{AcceptanceID: "acceptance_original"}}}, Deployment: &protocol.ObservedDeployment{ProofStatus: "unconfirmed", Runner: &protocol.ObservedRunner{HostID: "host_one", PendingOperations: &pending}}})
	}))
	defer srv.Close()
	r := Runner{c: &client{base: srv.URL, http: srv.Client()}}
	as := protocol.Assignment{RunID: "run_one", RunToken: "run-secret", Spec: json.RawMessage(`{}`)}
	if r.observationReader(t.Context(), as) != nil {
		t.Fatal("scope-less reader")
	}
	as.Spec = json.RawMessage(`{"observation":{"deployment":true}}`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	read := r.observationReader(ctx, as)
	for i := 1; i <= 2; i++ {
		got, err := read(t.Context())
		if err != nil || got.Seq != int64(i) {
			t.Fatalf("snapshot: %+v %v", got, err)
		}
		if got.DeliveryAuthorization == nil || got.DeliveryAuthorization.SourceTaskID != "task_source" || got.DeliveryAuthorization.Valid || got.DeliveryAuthorization.InvalidReason != "authorization_not_current" || got.Deployment.Runner.PendingOperations == nil || *got.Deployment.Runner.PendingOperations != 4 {
			t.Fatalf("runner dropped structured authorization or recovery facts: %+v", got)
		}
		if len(got.SharedReviews) != 1 || got.SharedReviews[0].Round.ReviewerRole != protocol.RoleAccept || got.SharedReviews[0].Round.ReviewerProfile.Effort != "xhigh" || got.SharedReviews[0].Round.ArtifactID != "actual-artifact" || got.SharedReviews[0].RoundLimit != 2 {
			t.Fatal("ordinary acceptance binding lost", got.SharedReviews)
		}
		if got.DeliveryAuthorization.Provider.CI.State != "failure" || got.DeliveryAuthorization.SharedReview.Valid || got.DeliveryAuthorization.SharedReview.Round.AcceptanceID != "acceptance_original" {
			t.Fatalf("runner dropped CI or review provenance: %+v", got.DeliveryAuthorization)
		}
	}
	status = http.StatusForbidden
	if _, err := read(t.Context()); err == nil || strings.Contains(err.Error(), "run-secret") {
		t.Fatalf("unsafe rejection %v", err)
	}
	cancel()
	if _, err := read(t.Context()); err == nil {
		t.Fatal("read after run cancellation")
	}
	if calls != 3 {
		t.Fatalf("requests=%d", calls)
	}
}
