package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIndependentDeliverySettlesTwoPRsInOneOwnerRun(t *testing.T) {
	h, runtime, issue, producer, first, register := ownerBoundDeliveryFixtureWithRuntime(t, false)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue.Plan.Deliveries = append(issue.Plan.Deliveries, protocol.IssueDelivery{
		DeliveryID: "other-pr", Provider: "github", Repository: "example/repo", Number: 18, URL: "https://example.invalid/repo/pull/18",
	})
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	second := first
	second.DeliveryID, second.URL, second.HeadSHA = "other-pr", issue.Plan.Deliveries[1].URL, strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, issue.IssueID, second, "open", "")

	read := func(id string, revision int64) *protocol.DeliveryExecutionAuthority {
		t.Helper()
		m, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "delivery_authorization", ID: id, DutyRevision: revision})
		if err != nil || m.DeliveryAuthorization == nil {
			t.Fatal("delivery readback", m, err)
		}
		return m.DeliveryAuthorization
	}
	envelope := func(p protocol.UpdateIssueDeliveryPayload) *protocol.Envelope {
		t.Helper()
		current := canonicalTaskIssue(t, h, issue.IssueID)
		p.TaskID, p.ExpectedAssignmentEpoch, p.Reason = producer.TaskID, current.AssignmentEpoch, "Exercise the real same-Task delivery transaction"
		return h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(current.Revision))
	}
	failure := func(principal protocol.Principal, env *protocol.Envelope, code protocol.Code) {
		t.Helper()
		op, err := h.app.Execute(t.Context(), principal, env, UpdateIssueDelivery{})
		if err == nil && op.Error != nil {
			err = op.Error
		}
		if protocol.CodeOf(err) != code {
			t.Fatalf("got %v (%+v), want %s", err, op, code)
		}
	}
	apply := func(p protocol.UpdateIssueDeliveryPayload) protocol.Operation {
		t.Helper()
		op := h.exec(owner, envelope(p), UpdateIssueDelivery{})
		if op.Status != protocol.OpSucceeded {
			t.Fatalf("action %s failed: %+v", p.Action, op)
		}
		return op
	}
	merge := func(c protocol.IssueDeliveryCandidate, sha string) {
		t.Helper()
		c.TargetSHA = strings.Repeat(sha, 40)
		observeDeliveryForTest(t, h, issue.IssueID, c, "merged", c.TargetSHA)
		recordOwnerPRChecksForTest(t, h, issue.IssueID, c)
	}

	bound := read(producer.TaskID, 0)
	begin := protocol.UpdateIssueDeliveryPayload{
		Action: "start_next", RunID: producer.RunID, Candidate: &second, ExpectedTaskControlRevision: rev(h.task(producer.TaskID).ControlRevision),
		PreviousExecution: &protocol.DeliveryExecutionSelector{ExecutionRunID: producer.RunID, DutyRevision: bound.DutyRevision},
	}
	failure(owner, envelope(begin), protocol.CodeInvalidTransition)
	register("Accepted original result for PR A")
	merge(first, "c")
	apply(protocol.UpdateIssueDeliveryPayload{Action: "complete"})
	original := read(producer.TaskID, 0)
	if original.SettledExecution == nil || *original.SettledExecution != *begin.PreviousExecution || original.Duty.Status != "complete" {
		t.Fatalf("original did not settle through the application: %+v", original)
	}
	failure(owner, envelope(protocol.UpdateIssueDeliveryPayload{Action: "prepare", Candidate: &second, ExpectedTaskControlRevision: begin.ExpectedTaskControlRevision}), protocol.CodeInvalidTransition)
	same := begin
	same.Candidate = &first
	failure(owner, envelope(same), protocol.CodeInvalidTransition)
	staleTask := begin
	staleTask.ExpectedTaskControlRevision = rev(*begin.ExpectedTaskControlRevision - 1)
	failure(owner, envelope(staleTask), protocol.CodeStaleRevision)
	staleIssue := envelope(begin)
	staleIssue.ExpectedControlRevision = rev(*staleIssue.ExpectedControlRevision - 1)
	failure(owner, staleIssue, protocol.CodeStaleRevision)
	staleEpoch := envelope(begin)
	var stalePayload protocol.UpdateIssueDeliveryPayload
	if err := json.Unmarshal(staleEpoch.Payload, &stalePayload); err != nil {
		t.Fatal(err)
	}
	stalePayload.ExpectedAssignmentEpoch--
	staleEpoch.Payload, _ = json.Marshal(stalePayload)
	failure(owner, staleEpoch, protocol.CodeStaleRevision)
	failure(runPrincipal(producer.RunID, producer.Role), envelope(begin), protocol.CodeForbidden)
	noActual := begin
	noActual.RunID = "no-current-run"
	failure(owner, envelope(noActual), protocol.CodeNotFound)

	unknown := h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'turn','{}','unknown',?)`, unknown, producer.RunID, h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	failure(owner, envelope(begin), protocol.CodeUnknownOutcome)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE manager_native_requests SET status='completed' WHERE request_id=?`, unknown)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Make independent review binding an explicit requirement for B.
	if op := policyChange(t, h, canonicalTaskIssue(t, h, issue.IssueID), true); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// The first review cannot authorize the second PR, even on the same Task.
	failure(owner, envelope(begin), protocol.CodeInvalidTransition)
	if after := read(producer.TaskID, 0); !reflect.DeepEqual(original.Duty, after.Duty) || len(after.History) != 0 {
		t.Fatal("failed requests changed the original settlement")
	}
	reviewOwnerPRForTest(t, h, runtime, issue.IssueID, producer.TaskID, second.DeliveryID)
	envs := [2]*protocol.Envelope{envelope(begin), envelope(begin)}
	var wg sync.WaitGroup
	var results [2]protocol.Operation
	var failures [2]error
	for i, env := range envs {
		wg.Go(func() { results[i], failures[i] = h.app.Execute(t.Context(), owner, env, UpdateIssueDelivery{}) })
	}
	wg.Wait()
	winner := 0
	if results[1].Status == protocol.OpSucceeded {
		winner = 1
	}
	loser := 1 - winner
	if failures[loser] == nil && results[loser].Error != nil {
		failures[loser] = results[loser].Error
	}
	if results[winner].Status != protocol.OpSucceeded || protocol.CodeOf(failures[loser]) != protocol.CodeStaleRevision {
		t.Fatalf("expected one committed start: results %+v, errors %v", results, failures)
	}
	replay := h.exec(owner, envs[winner], UpdateIssueDelivery{})
	if string(replay.Result) != string(results[winner].Result) {
		t.Fatal("exact replay did not return the original start receipt", replay)
	}
	pending := read(producer.TaskID, 0)
	if pending.Valid || pending.Duty == nil || pending.Duty.InstanceID == "" || pending.Duty.AttentionID == original.Duty.AttentionID ||
		pending.Duty.Revision <= original.Duty.Revision || pending.Duty.Candidate.DeliveryID != second.DeliveryID || len(pending.History) != 1 {
		t.Fatalf("new duty borrowed old authority or identity: %+v", pending)
	}
	failure(owner, envelope(begin), protocol.CodeInvalidTransition)
	bind := protocol.UpdateIssueDeliveryPayload{Action: "bind", ExecutionTaskID: producer.TaskID, ExpectedTaskControlRevision: begin.ExpectedTaskControlRevision, InstanceID: pending.Duty.InstanceID}
	old := bind
	old.InstanceID = ""
	failure(owner, envelope(old), protocol.CodeStaleRevision)
	failure(owner, envelope(bind), protocol.CodeForbidden)
	answerDeliveryForTest(t, h, *pending.Duty, "approve", "")
	failure(owner, envelope(bind), protocol.CodeInvalidTransition)
	recordOwnerPRChecksForTest(t, h, issue.IssueID, second)
	apply(bind)
	secondBound := read(producer.TaskID, 0)
	if !secondBound.Valid || secondBound.Approval == nil || secondBound.Approval.DecisionID == original.Approval.DecisionID {
		t.Fatalf("B lacks a separate actual approval: %+v", secondBound)
	}
	failure(owner, envelope(protocol.UpdateIssueDeliveryPayload{Action: "complete"}), protocol.CodeStaleRevision)
	merge(second, "f")
	complete := protocol.UpdateIssueDeliveryPayload{Action: "complete", InstanceID: pending.Duty.InstanceID}
	failure(owner, envelope(complete), protocol.CodeInvalidTransition)
	apply(protocol.UpdateIssueDeliveryPayload{Action: "submit_result", RunID: producer.RunID, InstanceID: pending.Duty.InstanceID,
		Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Actual B result verified independently of the original delivery"}}})
	failure(owner, envelope(complete), protocol.CodeInvalidTransition)
	_, reviewerArtifact := reviewOwnerArtifactForTest(t, h, runtime, issue.IssueID, producer, h.task(producer.TaskID).CurrentArtifactID)
	complete.ReviewerArtifactID = reviewerArtifact
	apply(complete)
	settled := read(producer.TaskID, 0)
	historical := read(original.SettledExecution.ExecutionRunID, original.SettledExecution.DutyRevision)
	if settled.SettledExecution == nil || settled.SettledExecution.ExecutionRunID != producer.RunID || settled.SettledExecution.DutyRevision <= original.SettledExecution.DutyRevision ||
		settled.Duty.Status != "complete" || settled.Duty.ArtifactID == original.Duty.ArtifactID || settled.Duty.Candidate.DeliveryID != second.DeliveryID ||
		!historical.Historical || historical.Valid || !reflect.DeepEqual(historical.Duty, original.Duty) ||
		!reflect.DeepEqual(historical.SettledExecution, original.SettledExecution) || historical.Settlement.ArtifactID != original.Duty.ArtifactID {
		t.Fatalf("two settlement facts differ from their original receipts: A=%+v B=%+v", historical, settled)
	}
	if h.count("issue_delivery_executions") != 1 || h.count("issue_delivery_execution_history") != 1 || h.count("upgrades") != 0 || h.task(producer.TaskID).Lifecycle.Terminal() {
		t.Fatal("the same Task did not retain one current execution and one immutable history")
	}
}
