package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const (
	prWaitCheck    = "integration-check"
	prWaitWorkflow = 76231873298
	prWaitURL      = "https://example.com/project/pull/7"
)

type prWaitTest struct {
	h     *harness
	rt    *protocol.RunnerRuntime
	issue protocol.Issue
	run   protocol.Assignment
	ref   protocol.IssueDelivery
}

func newPRWaitTest(t *testing.T, server bool) prWaitTest {
	t.Helper()
	h, rt := managerClaimHarness(t)
	h.app.serverDeliveryObservations = server
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityDeliveryGitHub,
		protocol.CapabilityDeliveryCandidateIdentity, protocol.CapabilityDeliveryTargetLineage, protocol.CapabilityDeliveryPRWaitEvidence)
	rt.Sequence++
	if op := h.versionedHello("linux-1", rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	ref := protocol.IssueDelivery{DeliveryID: "delivery-1", Provider: "github", Repository: "example/project", Number: 7, URL: prWaitURL}
	payload := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "PR observation", Project: "default",
		Intent: "Decide on the current candidate", Deliveries: []protocol.IssueDelivery{ref}}}
	issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil), historicalAdmittedIssueFixture{}))
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID},
		protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Own this delivery"}, rev(issue.Revision)), RequestIssueManager{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	run := ownerClaim(t, h, rt)
	if run.Manager == nil {
		t.Fatal(run)
	}
	return prWaitTest{h: h, rt: rt, issue: issue, run: run, ref: ref}
}

func prWaitSnapshot() protocol.DeliverySnapshot {
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	return protocol.DeliverySnapshot{State: "open", HeadSHA: head, BaseSHA: base, TargetSHA: base,
		TargetRef: "integration", Mergeability: "mergeable", URL: prWaitURL,
		CI: &protocol.DeliveryCI{HeadSHA: head, State: "pending", Checks: []protocol.DeliveryCheck{
			{ID: "check-pending", Name: prWaitCheck, State: "in_progress", Source: "github_check_run", App: "github-actions", WorkflowRunID: prWaitWorkflow},
		}},
	}
}

func (f prWaitTest) record(t *testing.T, snap *protocol.DeliverySnapshot, kind string) []protocol.PRCheckAttempt {
	t.Helper()
	if f.h.app.serverDeliveryObservations {
		q, err := f.h.app.ClaimServerDelivery(t.Context())
		if err != nil || q == nil {
			t.Fatal("claim server observation", q, err)
		}
		if err := f.h.app.RecordServerDelivery(t.Context(), q.ID, snap, kind); err != nil {
			t.Fatal(err)
		}
		return q.CheckAttempts
	}
	as := pollDelivery(t, f.h, "linux-1", f.rt.InstanceID)
	if len(as) != 1 || !as[0].PRWaitEvidence {
		t.Fatal("claim runner observation", as)
	}
	a := as[0]
	p := protocol.DeliveryReportPayload{InstanceID: f.rt.InstanceID, WorkItemID: a.WorkItemID,
		DeliveryID: a.Delivery.DeliveryID, Generation: a.Generation, Nonce: a.Nonce, Snapshot: snap, ErrorKind: kind}
	if !accepted(t, f.h.exec(runner("linux-1"), f.h.env(protocol.OpDeliveriesReport,
		protocol.Target{Type: "host", ID: "linux-1"}, p, nil), ReportDelivery{})) {
		t.Fatal("original runner result was rejected")
	}
	return a.CheckAttempts
}

func (f prWaitTest) request(t *testing.T, key string, condition protocol.PRWaitCondition, retries ...string) protocol.ExternalWait {
	t.Helper()
	p := protocol.WaitExternalPayload{WaitKey: key, Kind: "pr", TargetID: f.ref.DeliveryID,
		Reason: "Wait for the specific candidate evidence", Recovery: "Read the first observed result", Condition: &condition}
	env := f.h.env(protocol.OpRunsWaitExternal, runTarget(f.run.RunID), p, nil)
	if len(retries) > 0 {
		env.RequestID = retries[0]
	}
	op := f.h.exec(runPrincipal(f.run.RunID, f.run.Role), env, WaitExternal{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var receipt protocol.ExternalWait
	if err := json.Unmarshal(op.Result, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func (f prWaitTest) readWait(t *testing.T) protocol.ExternalWait {
	t.Helper()
	list, err := f.h.app.ExternalWaits(t.Context(), f.run.TaskID)
	if err != nil || len(list) != 1 {
		t.Fatal("read original wait", list, err)
	}
	return list[0]
}

func (f prWaitTest) sweep(t *testing.T) {
	t.Helper()
	if err := f.h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	system := protocol.Principal{Kind: protocol.PrincipalSystem}
	cmd := SweepIssueStatusNotifications{}
	op := f.h.exec(system, f.h.env(cmd.Operation(), protocol.Target{Type: "issues"}, nil, nil), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestPRWaitPinsFirstAcceptedAttemptBeforePhysicalStopThroughRestart(t *testing.T) {
	for _, path := range []string{"server", "runner"} {
		t.Run(path, func(t *testing.T) {
			f := newPRWaitTest(t, path == "server")
			o := prWaitSnapshot()
			f.record(t, &o, "")
			condition := protocol.PRWaitCondition{Kind: protocol.PRWaitCheckCompleted, HeadSHA: o.HeadSHA, Name: prWaitCheck,
				Source: "github_check_run", App: "github-actions", WorkflowRunID: prWaitWorkflow, Attempt: 2}
			wait := f.request(t, "exact-check", condition)
			if wait.Status != "stopping" || wait.NextReminderAtMillis != 0 || wait.PREvidence != nil {
				t.Fatal(wait)
			}
			if again := f.request(t, "exact-check", condition, wait.WaitID); again.WaitID != wait.WaitID {
				t.Fatal("duplicate request did not reuse wait identity", again)
			}
			o.CI.AttemptChecks = []protocol.DeliveryCheck{o.CI.Checks[0]}
			check := &o.CI.AttemptChecks[0]
			check.State, check.Conclusion, check.Attempt, check.ID = "completed", "success", 1, "earlier-job"
			o.CI.State = "success"
			f.h.clock.Advance(6 * time.Minute)
			attempts := f.record(t, &o, "")
			want := []protocol.PRCheckAttempt{{HeadSHA: o.HeadSHA, Name: prWaitCheck, WorkflowRunID: prWaitWorkflow, Attempt: 2}}
			if !slices.Equal(attempts, want) || f.readWait(t).PREvidence != nil {
				t.Fatal("old attempt matched or pinned attempt was not queried", attempts, f.readWait(t))
			}
			o.CI.HeadSHA = strings.Repeat("e", 40)
			check.Attempt = 2
			f.h.clock.Advance(6 * time.Minute)
			f.record(t, &o, "")
			if f.readWait(t).PREvidence != nil {
				t.Fatal("foreign head's check matched")
			}
			o.CI.HeadSHA = o.HeadSHA
			o.CI.Truncated, o.CI.AttemptChecks = true, nil
			f.h.clock.Advance(6 * time.Minute)
			f.record(t, &o, "")
			if f.readWait(t).PREvidence != nil {
				t.Fatal("truncated current rollup without requested attempt evidence matched")
			}
			o.CI.Truncated = false
			check.Conclusion, check.ID, check.URL = "failure", "attempt-two-job", prWaitURL+"/checks/2"
			o.CI.AttemptChecks = []protocol.DeliveryCheck{*check}
			o.CI.State, o.CI.Checks[0].State, o.CI.Checks[0].ID = "pending", "in_progress", "attempt-three-job"
			f.h.clock.Advance(6 * time.Minute)
			f.record(t, &o, "")
			before := f.readWait(t)
			if before.Status != "stopping" || before.PREvidence == nil || before.PREvidence.Check == nil ||
				before.PREvidence.Check.ID != check.ID || before.PREvidence.Check.Conclusion != "failure" ||
				before.PREvidence.URL != check.URL || f.h.lease(f.run.RunID).Status != store.LeaseActive ||
				f.h.count("events WHERE kind='task.external_wait_ready'") != 0 {
				t.Fatal("capture must precede and remain distinct from physical stop", before)
			}
			reopenArchiveCombinationHarness(t, f.h)
			f.h.app.serverDeliveryObservations = path == "server"
			f.rt.Sequence++
			if op := f.h.versionedHello("linux-1", f.rt); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			// A later rerun and another provider read must leave the first proof intact.
			o.CI.AttemptChecks = nil
			o.CI.Checks[0].State, o.CI.Checks[0].Conclusion = "completed", "success"
			f.h.clock.Advance(6 * time.Minute)
			f.record(t, &o, "")
			f.sweep(t)
			if next := ownerClaim(t, f.h, f.rt); next.RunID != "" {
				t.Fatal("a new Run replaced an active original", next)
			}
			stopOwnerClaim(t, f.h, f.run)
			f.sweep(t)
			f.sweep(t)
			f.rt.Sequence++
			if refreshed := f.h.versionedHello("linux-1", f.rt); refreshed.Status != protocol.OpSucceeded {
				t.Fatal(refreshed)
			}
			after := f.readWait(t)
			if after.Status != "continued" || after.WakeReason != "check_completed" || after.SourceEvent == "" || after.SourceMessage == "" ||
				after.PREvidence.FactRevision != before.PREvidence.FactRevision || after.PREvidence.Check.ID != "attempt-two-job" ||
				f.h.lease(f.run.RunID).Status != store.LeaseReleased || f.h.count("events WHERE kind='task.external_wait_ready'") != 1 {
				t.Fatal("failed to continue with the original first proof", after)
			}
			next := ownerClaim(t, f.h, f.rt)
			if next.RunID == "" || next.RunID == f.run.RunID || next.TaskID != f.run.TaskID || next.ExternalWaitContinuation == nil ||
				next.ExternalWaitContinuation.WaitID != wait.WaitID || next.ExternalWaitContinuation.PREvidence.Check.ID != "attempt-two-job" {
				t.Fatal("new execution lost the original Task, wait or provider evidence", next)
			}
			if next := ownerClaim(t, f.h, f.rt); next.RunID != "" {
				t.Fatal("repeated read admitted duplicate owner", next)
			}
		})
	}
}

func TestPRWaitKnownResultKeepsCurrentRunAndAllowsAnotherCondition(t *testing.T) {
	f := newPRWaitTest(t, true)
	o := prWaitSnapshot()
	o.CI.State = "failure"
	o.CI.Checks[0].State, o.CI.Checks[0].Conclusion = "completed", "skipped"
	f.record(t, &o, "")
	condition := protocol.PRWaitCondition{Kind: protocol.PRWaitCheckCompleted, HeadSHA: o.HeadSHA, Name: prWaitCheck}
	first := f.request(t, "known-check", condition)
	if first.Status != protocol.ExternalWaitSatisfied || first.PREvidence == nil || first.PREvidence.Check.Conclusion != "skipped" ||
		f.h.lease(f.run.RunID).Status != store.LeaseActive || f.h.count("run_external_waits") != 0 {
		t.Fatal(first)
	}
	retry := f.request(t, "known-check", condition, first.WaitID)
	if retry.WaitID != first.WaitID || f.h.count("events WHERE kind='run.pr_wait_satisfied'") != 1 || f.h.count("events WHERE kind='task.external_wait_ready'") != 0 {
		t.Fatal("the original immediate result was not returned", retry)
	}
	different := protocol.WaitExternalPayload{WaitKey: "known-check", Kind: "task", TargetID: "unrelated",
		Reason: "Different wait", Recovery: "Use the original immutable identity"}
	_, err := f.h.app.Execute(t.Context(), runPrincipal(f.run.RunID, f.run.Role),
		f.h.env(protocol.OpRunsWaitExternal, runTarget(f.run.RunID), different, nil), WaitExternal{})
	if protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("the settled key admitted a different condition", err)
	}
	wait := f.request(t, "future-merge", protocol.PRWaitCondition{Kind: protocol.PRWaitMerged})
	if wait.Status != "stopping" || wait.WaitID == first.WaitID {
		t.Fatal("the immediate read took the Run's physical wait", wait)
	}
	list, err := f.h.app.ExternalWaits(t.Context(), f.run.TaskID)
	if err != nil || len(list) != 2 || !slices.ContainsFunc(list, func(v protocol.ExternalWait) bool {
		return v.WaitID == first.WaitID && v.Status == protocol.ExternalWaitSatisfied
	}) {
		t.Fatal("original evidence is absent from the wait inspector", list, err)
	}
}

func TestPRWaitNoImplicitReminderAndOwnerInputContinues(t *testing.T) {
	f := newPRWaitTest(t, true)
	o := prWaitSnapshot()
	f.record(t, &o, "")
	wait := f.request(t, "context", protocol.PRWaitCondition{Kind: protocol.PRWaitCheckCompleted, HeadSHA: o.HeadSHA, Name: prWaitCheck})
	stopOwnerClaim(t, f.h, f.run)
	f.h.clock.Advance(90 * time.Minute)
	f.sweep(t)
	if v := f.readWait(t); v.Status != "waiting" || v.PREvidence != nil || f.h.count("events WHERE kind='task.external_wait_ready'") != 0 {
		t.Fatal("undelivered check received a timer wake", v)
	}
	queueOwnerWaitInput(t, f.h, f.run.Manager.ConversationID)
	f.h.clock.Advance(5 * time.Second)
	f.sweep(t)
	v := f.readWait(t)
	if v.WaitID != wait.WaitID || v.WakeReason != "owner_input" || v.Status != "continued" ||
		f.h.count("events WHERE kind='task.external_wait_ready'") != 1 {
		t.Fatal("owner input did not continue this original Task", v)
	}
}

func TestPRWaitCandidateAndTerminalConditionsUseTheirOwnIdentities(t *testing.T) {
	base := prWaitSnapshot()
	for _, tc := range []struct {
		name      string
		condition protocol.PRWaitCondition
		change    func(*protocol.DeliverySnapshot)
		reason    string
	}{
		{"conflict appears", protocol.PRWaitCondition{Kind: protocol.PRWaitConflictAppears, HeadSHA: base.HeadSHA}, func(o *protocol.DeliverySnapshot) { o.Mergeability = "conflicting" }, "conflict_appears"},
		{"conflict clears", protocol.PRWaitCondition{Kind: protocol.PRWaitConflictClears, HeadSHA: base.HeadSHA}, func(o *protocol.DeliverySnapshot) { o.Mergeability = "mergeable" }, "conflict_clears"},
		{"head drift", protocol.PRWaitCondition{Kind: protocol.PRWaitCheckCompleted, HeadSHA: base.HeadSHA, Name: prWaitCheck}, func(o *protocol.DeliverySnapshot) { o.HeadSHA = strings.Repeat("c", 40) }, "candidate_changed"},
		{"candidate target drift", protocol.PRWaitCondition{Kind: protocol.PRWaitCheckCompleted, HeadSHA: base.HeadSHA, Name: prWaitCheck}, func(o *protocol.DeliverySnapshot) { o.TargetRef = "staging" }, "candidate_changed"},
		{"observe exact target move", protocol.PRWaitCondition{Kind: protocol.PRWaitTargetMoved, TargetRef: "integration", TargetSHA: base.TargetSHA}, func(o *protocol.DeliverySnapshot) { o.TargetSHA = strings.Repeat("c", 40) }, "target_moved"},
		{"other target", protocol.PRWaitCondition{Kind: protocol.PRWaitTargetMoved, TargetRef: "integration", TargetSHA: base.TargetSHA}, func(o *protocol.DeliverySnapshot) { o.TargetRef = "staging"; o.TargetSHA = strings.Repeat("c", 40) }, ""},
		{"merged later head", protocol.PRWaitCondition{Kind: protocol.PRWaitMerged}, func(o *protocol.DeliverySnapshot) { o.HeadSHA = strings.Repeat("c", 40); o.State = "merged" }, "pull_request_merged"},
		{"closed", protocol.PRWaitCondition{Kind: protocol.PRWaitClosed}, func(o *protocol.DeliverySnapshot) { o.State = "closed" }, "pull_request_closed"},
		{"merged is distinct from closed", protocol.PRWaitCondition{Kind: protocol.PRWaitClosed}, func(o *protocol.DeliverySnapshot) { o.State = "merged" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPRWaitTest(t, true)
			o := prWaitSnapshot()
			if tc.condition.Kind == protocol.PRWaitConflictClears {
				o.Mergeability = "conflicting"
			}
			f.record(t, &o, "")
			if v := f.request(t, "watch", tc.condition); v.PREvidence != nil {
				t.Fatal("condition already matched", v)
			}
			stopOwnerClaim(t, f.h, f.run)
			tc.change(&o)
			f.h.clock.Advance(6 * time.Minute)
			f.record(t, &o, "")
			v := f.readWait(t)
			if v.WakeReason != tc.reason || (v.PREvidence == nil) != (tc.reason == "") {
				t.Fatal("unexpected provider evidence", v, tc.reason)
			}
		})
	}
}

func TestPRWaitDeadlineRejectsLaterFactWhileOriginalIsStillStopping(t *testing.T) {
	f := newPRWaitTest(t, true)
	o := prWaitSnapshot()
	f.record(t, &o, "")
	p := protocol.WaitExternalPayload{WaitKey: "check-before-cutoff", Kind: "pr", TargetID: f.ref.DeliveryID,
		Reason: "Allow time for the original check", Recovery: "Report the outcome by the deadline",
		DeadlineAtMillis: f.h.clock.Now().Add(time.Minute).UnixMilli(),
		Condition:        &protocol.PRWaitCondition{Kind: protocol.PRWaitCheckCompleted, Name: prWaitCheck, HeadSHA: o.HeadSHA}}
	if op := f.h.runAs(f.run, protocol.OpRunsWaitExternal, p, WaitExternal{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	f.h.clock.Advance(2 * time.Minute)
	o.CI.Checks[0].State, o.CI.Checks[0].Conclusion = "completed", "cancelled"
	f.record(t, &o, "")
	if v := f.readWait(t); v.PREvidence != nil || v.Status != "stopping" {
		t.Fatal("late result replaced the deadline or bypassed stop", v)
	}
	stopOwnerClaim(t, f.h, f.run)
	if v := f.readWait(t); v.WakeReason != "deadline" || v.Status != "continued" || v.PREvidence != nil {
		t.Fatal("the ordinary explicit deadline did not resume the original Task", v)
	}
}

func TestPRWaitTargetRefRemainsObservableAfterPRMerged(t *testing.T) {
	for _, server := range []bool{false, true} {
		t.Run(map[bool]string{false: "runner", true: "server"}[server], func(t *testing.T) {
			f := newPRWaitTest(t, server)
			o := prWaitSnapshot()
			o.State, o.MergeSHA, o.TargetContainsMerge = "merged", o.TargetSHA, true
			f.record(t, &o, "")
			wait := f.request(t, "observe-branch", protocol.PRWaitCondition{Kind: protocol.PRWaitTargetMoved,
				TargetSHA: o.TargetSHA, TargetRef: o.TargetRef})
			stopOwnerClaim(t, f.h, f.run)
			f.record(t, &o, "")
			if v := f.readWait(t); v.Status != "waiting" {
				t.Fatal(v)
			}
			o.TargetSHA = strings.Repeat("f", 40)
			// The provider already returned the new ref tip. A separate
			// lineage API failure still blocks formal merge fulfillment.
			o.LineageErrorKind, o.TargetContainsMerge = "query_failed", false
			f.h.clock.Advance(6 * time.Minute)
			f.record(t, &o, "")
			if v := f.readWait(t); v.WaitID != wait.WaitID || v.WakeReason != "target_moved" || v.PREvidence.TargetSHA != o.TargetSHA {
				t.Fatal("explicit target observation stopped at PR merge", v)
			}
		})
	}
}

func TestUnsubscribedRunnerQueriesLeaveCanonicalOwnerIdle(t *testing.T) {
	f := newPRWaitTest(t, false)
	stopOwnerClaim(t, f.h, f.run)
	f.h.clock.Advance(6 * time.Minute)
	f.sweep(t)
	initial := issueChangeSources(t, f.h, f.issue.IssueID)
	o := prWaitSnapshot()
	f.record(t, &o, "")
	f.h.clock.Advance(6 * time.Minute)
	o.CI.Checks[0].State, o.CI.Checks[0].Conclusion = "completed", "skipped"
	o.Mergeability = "conflicting"
	f.record(t, &o, "")
	f.h.clock.Advance(6 * time.Minute)
	f.sweep(t)
	if n := f.h.count("events WHERE kind='work_item.delivery_query_started'"); n != 2 {
		t.Fatal("fixture did not exercise runner query start", n)
	}
	if n := issueChangeSources(t, f.h, f.issue.IssueID); n != initial || f.h.count("runs") != 1 {
		t.Fatal("runner query or provider result woke the canonical owner", initial, n)
	}
}

func TestPRWaitPrefersCapableRunnerAndRetainsLegacyObservationFallback(t *testing.T) {
	for _, modern := range []bool{true, false} {
		t.Run(map[bool]string{false: "legacy-only", true: "mixed-generations"}[modern], func(t *testing.T) {
			f := newPRWaitTest(t, false)
			o := prWaitSnapshot()
			f.record(t, &o, "")
			f.request(t, "needs-check", protocol.PRWaitCondition{Kind: protocol.PRWaitCheckCompleted, HeadSHA: o.HeadSHA, Name: prWaitCheck})
			stopOwnerClaim(t, f.h, f.run)

			cfg := testConfig()
			old := cfg.Hosts[0]
			old.HostID = "linux-2"
			cfg.Hosts = append(cfg.Hosts, old)
			f.h.syncConfig(cfg)
			if !modern {
				f.h.clock.Advance(RunnerOfflineAfter)
			}
			observerHello(t, f.h, "linux-2", "legacy-observer")
			legacy := pollDelivery(t, f.h, "linux-2", "legacy-observer")
			if modern {
				if len(legacy) != 0 {
					t.Fatal("a legacy reader reserved the available exact-evidence query", legacy)
				}
				o.CI.Checks[0].State, o.CI.Checks[0].Conclusion = "completed", "failure"
				f.record(t, &o, "")
				if v := f.readWait(t); v.WakeReason != "check_completed" || v.PREvidence.Check.Conclusion != "failure" {
					t.Fatal("new generation could not resolve the held wait", v)
				}
				return
			}
			if len(legacy) != 1 || legacy[0].PRWaitEvidence {
				t.Fatal("no modern reader is live, so the original legacy path should still observe facts", legacy)
			}
			rep := protocol.DeliveryReportPayload{InstanceID: "legacy-observer", WorkItemID: legacy[0].WorkItemID,
				DeliveryID: legacy[0].Delivery.DeliveryID, Generation: legacy[0].Generation, Nonce: legacy[0].Nonce,
				Snapshot: &protocol.DeliverySnapshot{State: "open", HeadSHA: o.HeadSHA, TargetSHA: o.TargetSHA, Mergeability: "conflicting"}}
			if !accepted(t, f.h.exec(runner("linux-2"), f.h.env(protocol.OpDeliveriesReport, protocol.Target{Type: "host", ID: "linux-2"}, rep, nil), ReportDelivery{})) {
				t.Fatal("capability preference rejected an original legacy receipt")
			}
			if v := f.readWait(t); v.Status != "waiting" || v.PREvidence != nil {
				t.Fatal("legacy observation fabricated new check evidence", v)
			}
			if current := readObservations(t, f.h, f.issue.IssueID)[0]; current.Mergeability != "conflicting" {
				t.Fatal("legacy observation stopped updating ordinary views", current)
			}
		})
	}
}
