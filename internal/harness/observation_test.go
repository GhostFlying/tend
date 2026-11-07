package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestObservationScopeArgumentsAndFailure(t *testing.T) {
	calls := 0
	spec := WorkerSpec{Spec: json.RawMessage(`{"observation":{"work_item_ids":["wi_one"]}}`), ReadObservation: func(context.Context) (protocol.RunObservation, error) {
		calls++
		return protocol.RunObservation{Version: 1, Seq: int64(calls)}, nil
	}}
	r := Results{Spec: spec}
	unavailable := spec
	unavailable.ReadObservation = nil
	if HasObservation(unavailable) {
		t.Fatal("scope alone exposed observation")
	}
	unavailable = spec
	unavailable.Spec = json.RawMessage(`{}`)
	if HasObservation(unavailable) {
		t.Fatal("callback alone exposed observation")
	}
	for _, args := range []string{`null`, `{"work_item_id":"wi_other"}`, `[]`, `{}`} {
		got := r.Handle(t.Context(), ToolCall{Tool: ReadObservationTool, Arguments: json.RawMessage(args)})
		if got.Success != (args == "{}") {
			t.Fatalf("%s: %+v", args, got)
		}
	}
	got := r.Handle(t.Context(), ToolCall{Tool: ReadObservationTool, Arguments: json.RawMessage(`{}`)})
	if calls != 2 || !strings.Contains(got.Text, `"seq":2`) {
		t.Fatalf("not refreshed: %+v calls=%d", got, calls)
	}
	r.Spec.Spec = json.RawMessage(`{}`)
	if got := r.Handle(t.Context(), ToolCall{Tool: ReadObservationTool, Arguments: json.RawMessage(`{}`)}); got.Success || calls != 2 {
		t.Fatal("scope-less read allowed")
	}
	r.Spec = spec
	r.Spec.ReadObservation = func(context.Context) (protocol.RunObservation, error) {
		return protocol.RunObservation{}, errors.New("secret bearer credential")
	}
	if got := r.Handle(t.Context(), ToolCall{Tool: ReadObservationTool, Arguments: json.RawMessage(`{}`)}); got.Success || strings.Contains(got.Text, "secret") || strings.Contains(got.Text, "credential") {
		t.Fatalf("unsafe error: %+v", got)
	}
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		spec.Role = role
		instructions, input := Brief(spec)
		if role == protocol.RoleAccept && (!strings.Contains(instructions, "submit_verdict evidence.delivery_review") || !strings.Contains(instructions, "Do not call submit_result")) {
			t.Fatal("acceptance guidance selected the report role", instructions)
		}
		if !strings.Contains(input, "wi_one") || !strings.Contains(input, "does not prove browser") || !strings.Contains(input, "independently refresh") {
			t.Fatal(input)
		}
		if !strings.Contains(input, "delivery_authorization") || !strings.Contains(input, "not a task-spec field") || !strings.Contains(input, "unconfirmed is not itself a merge denial") {
			t.Fatal("worker guidance still asks for owner Task CLI or treats deployment proof as merge authority", input)
		}
		if !strings.Contains(input, "provider.ci") || !strings.Contains(input, "shared_review") {
			t.Fatal("worker guidance omitted the actual checks and formal review provenance", input)
		}
	}
}

func TestObservationToolPreservesStructuredAuthorityAndOldServerAbsence(t *testing.T) {
	for _, present := range []bool{false, true} {
		spec := WorkerSpec{Spec: json.RawMessage(`{"observation":{"deployment":true}}`), ReadObservation: func(context.Context) (protocol.RunObservation, error) {
			v := protocol.RunObservation{Version: 1, RunID: "run_one"}
			if present {
				v.SharedReviews = []protocol.ObservedDeliveryReview{{ReviewID: "accept-review", Repository: "example/repo", Status: "pending", RoundLimit: 2}}
				v.DeliveryAuthorization = &protocol.DeliveryExecutionAuthority{TaskID: "task_one", SourceTaskID: "task_original", DutyRevision: 2, Valid: false, InvalidReason: "provider_candidate_changed", Provider: &protocol.ObservedDeliveryProvider{CI: &protocol.DeliveryCI{HeadSHA: "original", State: "unknown"}}, SharedReview: &protocol.ObservedDeliveryReview{ReviewID: "review_original", Valid: false, Round: &protocol.ObservedDeliveryReviewRound{AcceptanceID: "acceptance_original", RepositoryCheck: "original repository check"}}}
			}
			return v, nil
		}}
		r := Results{Spec: spec}
		got := r.Handle(t.Context(), ToolCall{Tool: ReadObservationTool, Arguments: json.RawMessage(`{}`)})
		var decoded protocol.RunObservation
		if !got.Success || json.Unmarshal([]byte(got.Text), &decoded) != nil || (decoded.DeliveryAuthorization != nil) != present {
			t.Fatalf("tool changed field presence: %+v", got)
		}
		if (len(decoded.SharedReviews) > 0) != present {
			t.Fatal("ordinary acceptance projection presence changed", decoded)
		}
		if present && (decoded.SharedReviews[0].ReviewID != "accept-review" || decoded.SharedReviews[0].Round != nil || decoded.SharedReviews[0].Status != "pending") {
			t.Fatal("pending binding was invented or lost", decoded.SharedReviews)
		}
		if present && (decoded.DeliveryAuthorization.Valid || decoded.DeliveryAuthorization.InvalidReason != "provider_candidate_changed") {
			t.Fatal("rejected authority was promoted")
		}
		if present && (decoded.DeliveryAuthorization.Provider.CI.State != "unknown" || decoded.DeliveryAuthorization.SharedReview.Valid || decoded.DeliveryAuthorization.SharedReview.Round.AcceptanceID != "acceptance_original") {
			t.Fatal("actual CI or review provenance was dropped or promoted")
		}
	}
}
