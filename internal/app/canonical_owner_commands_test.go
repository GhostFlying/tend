package app

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func createCanonicalOwnerTestIssue(t *testing.T, h *harness) protocol.Issue {
	t.Helper()
	p := canonicalOwnerIssueCreate{Plan: protocol.WorkItemPlan{Title: "Investigate an intent", Project: "default", Intent: "Produce a verified result", Stages: []protocol.WorkItemStage{}}}
	op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Assignee.Kind != protocol.IssueAssigneeOwner || issue.Assignee.Ref != owner.Subject || issue.AssignmentEpoch != 1 || issue.Revision != 1 {
		t.Fatalf("created=%+v", issue)
	}
	return issue
}

func TestCanonicalOwnerDefinitionNoopReceiptSurvivesLaterRevision(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	target := protocol.Target{Type: "issue", ID: issue.IssueID}
	payload := canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}
	env := h.env("issues.update", target, payload, rev(issue.Revision))
	events, outbox, bindings := h.count("events"), h.count("outbox"), h.count("conversation_bindings")
	noOp := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "update"})
	if noOp.Status != protocol.OpSucceeded {
		t.Fatal(noOp)
	}
	if h.count("events") != events || h.count("outbox") != outbox || h.count("conversation_bindings") != bindings {
		t.Fatal("no-op emitted an effect")
	}
	var returned protocol.Issue
	if err := json.Unmarshal(noOp.Result, &returned); err != nil {
		t.Fatal(err)
	}
	if returned.Revision != issue.Revision || returned.AssignmentEpoch != issue.AssignmentEpoch {
		t.Fatal("no-op changed revision or epoch")
	}
	changed := payload
	changed.Plan.Title = "Clarified goal"
	op := h.exec(owner, h.env("issues.update", target, changed, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	counts := map[string]int{}
	for _, table := range []string{"request_records", "events", "outbox", "conversation_bindings", "work_item_revisions", "issue_control_revisions"} {
		counts[table] = h.count(table)
	}
	replay := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "update"})
	if replay.Status != protocol.OpSucceeded || !bytes.Equal(replay.Result, noOp.Result) {
		t.Fatalf("replay=%+v; original=%+v", replay, noOp)
	}
	for table, count := range counts {
		if h.count(table) != count {
			t.Fatalf("replay changed %s", table)
		}
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var n int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM request_records WHERE domain='owner' AND request_id=?`, env.RequestID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("no-op request count=%d", n)
		}
		got, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		if got.Revision != 2 || got.AssignmentEpoch != 1 {
			t.Fatalf("current=%+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalOwnerSamePlanSourceOnlyCreatesRevision(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	// Material keeps the assistant's identity. The current owner request supplies
	// authority; source edits below use the real streamed-message update path.
	input, turn := startReviewTurn(t, h)
	report := recordAgentMessagePayload{Body: "Investigation report", ReplyTo: input, TurnID: turn.TurnID, ItemID: "source-report", Status: protocol.AssistantStreaming}
	submit := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
	if submit.Status != protocol.OpSucceeded {
		t.Fatal(submit)
	}
	var message struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(submit.Result, &message); err != nil {
		t.Fatal(err)
	}
	p := canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, canonicalOwnerIssueSource: canonicalOwnerIssueSource{SourceMessageID: message.MessageID, ExpectedSourceRevision: rev(1)}}
	op := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		got, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		if got.Revision != 2 {
			t.Fatalf("source-only ignored: revision=%d", got.Revision)
		}
		historical, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), issue.IssueID, 2)
		if err != nil {
			return err
		}
		if historical.Plan.Title != issue.Plan.Title {
			t.Fatal("source-only changed plan")
		}
		var source, reason string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COALESCE(source_message_id,''),reason_json FROM issue_control_revisions WHERE issue_id=? AND revision=2`, issue.IssueID).Scan(&source, &reason); err != nil {
			return err
		}
		if source != message.MessageID {
			t.Fatal("source-only lost exact message")
		}
		var saved canonicalOwnerIssueUpdate
		if err := json.Unmarshal([]byte(reason), &saved); err != nil {
			return err
		}
		if saved.ExpectedSourceRevision == nil || *saved.ExpectedSourceRevision != 1 {
			t.Fatal("source-only lost content revision")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	report.Body = "Corrected investigation report"
	report.Status = protocol.AssistantCompleted
	corrected := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
	if corrected.Status != protocol.OpSucceeded {
		t.Fatal(corrected)
	}
	stale := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(2)), CanonicalOwnerIssueCommand{Action: "update"})
	expectWorkCode(t, stale, protocol.CodeStaleRevision)
	p.ExpectedSourceRevision = rev(2)
	changed := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(2)), CanonicalOwnerIssueCommand{Action: "update"})
	if changed.Status != protocol.OpSucceeded {
		t.Fatal(changed)
	}
	plain := p
	plain.canonicalOwnerIssueSource = canonicalOwnerIssueSource{}
	unchanged := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, plain, rev(3)), CanonicalOwnerIssueCommand{Action: "update"})
	if unchanged.Status != protocol.OpSucceeded {
		t.Fatal(unchanged)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		if current.Revision != 3 {
			t.Fatal("source omission changed or discarded provenance")
		}
		for revision, wantContent := range map[int64]int64{2: 1, 3: 2} {
			if _, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), issue.IssueID, revision); err != nil {
				return err
			}
			var source, reason string
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT COALESCE(source_message_id,''),reason_json FROM issue_control_revisions WHERE issue_id=? AND revision=?`, issue.IssueID, revision).Scan(&source, &reason); err != nil {
				return err
			}
			var evidence canonicalOwnerIssueUpdate
			if err := json.Unmarshal([]byte(reason), &evidence); err != nil {
				return err
			}
			if source != message.MessageID || evidence.ExpectedSourceRevision == nil || *evidence.ExpectedSourceRevision != wantContent {
				t.Fatalf("revision %d lost source version", revision)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalOwnerCreateReplayKeepsOneIdentity(t *testing.T) {
	h := newHarness(t)
	payload := canonicalOwnerIssueCreate{Plan: protocol.WorkItemPlan{Title: "Investigate an intent", Project: "default", Intent: "Produce an accepted report", Stages: []protocol.WorkItemStage{}}}
	envelope := h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil)
	first := h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "create"})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	before := canonicalGuardBusinessSnapshot(t, h)
	replay := h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "create"})
	if replay.Status != protocol.OpSucceeded || !bytes.Equal(first.Result, replay.Result) {
		t.Fatal(first, replay)
	}
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("create replay allocated another responsibility or effect")
	}
	var issue protocol.Issue
	if err := json.Unmarshal(first.Result, &issue); err != nil {
		t.Fatal(err)
	}
	if !protocol.ValidID(protocol.PrefixWorkItem, issue.IssueID) || issue.Revision != 1 || issue.AssignmentEpoch != 1 {
		t.Fatal(issue)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, table := range []string{"issue_controls", "issue_control_revisions", "work_item_revisions"} {
			var count int
			if err := tx.Q().QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Fatalf("%s count=%d", table, count)
			}
		}
		historical, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), issue.IssueID, 1)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(historical)
		if err != nil {
			return err
		}
		if !bytes.Equal(encoded, first.Result) {
			t.Fatal("current response and immutable first revision differ")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalOwnerUpdateFencesRevisionAndEpoch(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	payload := canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}
	payload.Plan.Title = "Clarified bounded investigation"
	target := protocol.Target{Type: "issue", ID: issue.IssueID}
	badEpoch := payload
	badEpoch.ExpectedAssignmentEpoch = issue.AssignmentEpoch + 1
	before := canonicalGuardBusinessSnapshot(t, h)
	op := h.exec(owner, h.env("issues.update", target, badEpoch, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	expectWorkCode(t, op, protocol.CodeStaleRevision)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("old epoch changed business state")
	}
	good := h.exec(owner, h.env("issues.update", target, payload, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	if good.Status != protocol.OpSucceeded {
		t.Fatal(good)
	}
	before = canonicalGuardBusinessSnapshot(t, h)
	stale := h.exec(owner, h.env("issues.update", target, payload, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	expectWorkCode(t, stale, protocol.CodeStaleRevision)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("stale update changed business state")
	}
}

func TestCanonicalOwnerCreateRejectsUnregisteredAssignment(t *testing.T) {
	for _, assignee := range []protocol.IssueAssignee{{Kind: protocol.IssueAssigneeAgent, Ref: "unregistered-agent"}, {Kind: protocol.IssueAssigneeOwner, Ref: "another-owner"}} {
		t.Run(string(assignee.Kind), func(t *testing.T) {
			h := newHarness(t)
			p := canonicalOwnerIssueCreate{Plan: protocol.WorkItemPlan{Title: "Bounded intent", Project: "default", Intent: "Produce a result", Stages: []protocol.WorkItemStage{}}, Assignee: &assignee}
			before := canonicalGuardBusinessSnapshot(t, h)
			op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
			expectWorkCode(t, op, protocol.CodeInvalidRequest)
			if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
				t.Fatal("unregistered assignment partially created an issue")
			}
		})
	}
}

func TestCanonicalOwnerCreateRejectsChildWaitingOnParentCompletion(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	p := canonicalOwnerIssueCreate{Plan: protocol.WorkItemPlan{Title: "Child investigation", Project: "default", Intent: "Produce a child result", Stages: []protocol.WorkItemStage{}}, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision), DependsOn: []string{parent.IssueID}}
	before := canonicalGuardBusinessSnapshot(t, h)
	rejected := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
	expectWorkCode(t, rejected, protocol.CodeInvalidRequest)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("completion cycle left a partial child")
	}
	p.DependsOn = nil
	if op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestCanonicalOwnerEventPersistenceFailureRollsBackBothSnapshots(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TEMP TRIGGER fail_canonical_issue_event BEFORE INSERT ON events WHEN NEW.kind='issue.created' BEGIN SELECT RAISE(FAIL,'injected issue event failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := canonicalOwnerIssueCreate{Plan: protocol.WorkItemPlan{Title: "Intent before event failure", Project: "default", Intent: "Produce an accepted report", Stages: []protocol.WorkItemStage{}}}
	before := canonicalGuardBusinessSnapshot(t, h)
	_, err := h.app.Execute(t.Context(), owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
	if err == nil {
		t.Fatal("injected event failure was hidden")
	}
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("persistOutcome failure escaped aggregate transaction")
	}
}

func TestCanonicalOwnerSourceRejectsInvalidReferences(t *testing.T) {
	for _, kind := range []string{"stale content", "withdrawn", "missing message", "missing source revision", "revision without source"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			submit := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Original owner clarification"}, nil), SubmitMessage{})
			if submit.Status != protocol.OpAccepted {
				t.Fatal(submit)
			}
			var source struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(submit.Result, &source); err != nil {
				t.Fatal(err)
			}
			p := canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, canonicalOwnerIssueSource: canonicalOwnerIssueSource{SourceMessageID: source.MessageID, ExpectedSourceRevision: rev(1)}}
			want := protocol.CodeInvalidRequest
			switch kind {
			case "stale content":
				p.ExpectedSourceRevision = rev(2)
				want = protocol.CodeStaleRevision
			case "missing message":
				p.SourceMessageID = "msg_01KDVDNA0048R1ZE6R55WDNWZZ"
			case "withdrawn":
				var control protocol.MessageControl
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					control, err = store.MessageControl(t.Context(), tx.Q(), source.MessageID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				cmd := WithdrawMessage{}
				if op := h.exec(owner, h.env(cmd.Operation(), protocol.Target{Type: "message", ID: source.MessageID}, protocol.WithdrawMessagePayload{}, rev(control.Revision)), cmd); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				want = protocol.CodeInvalidTransition
			case "missing source revision":
				p.ExpectedSourceRevision = nil
			case "revision without source":
				p.SourceMessageID = ""
			}
			before := canonicalGuardBusinessSnapshot(t, h)
			op := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
			expectWorkCode(t, op, want)
			if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
				t.Fatal("invalid owner source changed business state")
			}
		})
	}
}

func TestCanonicalOwnerCommandsRejectOtherPrincipalAndLifecycleAction(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	before := canonicalGuardBusinessSnapshot(t, h)
	payload := canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}
	env := h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision))
	if _, err := h.app.Execute(t.Context(), mainAgent, env, CanonicalOwnerIssueCommand{Action: "update"}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("main principal got canonical owner permission: %v", err)
	}
	unsupported := h.exec(owner, h.env("issues.cancel", env.Target, map[string]any{"expected_assignment_epoch": issue.AssignmentEpoch, "reason": "not implemented"}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "cancel"})
	expectWorkCode(t, unsupported, protocol.CodeInvalidRequest)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("disabled command changed business state")
	}
}

func TestCanonicalCreateDependencyOrderMatchesReceiptAndHistory(t *testing.T) {
	h := newHarness(t)
	first := createCanonicalOwnerTestIssue(t, h)
	second := createCanonicalOwnerTestIssue(t, h)
	expected := []string{first.IssueID, second.IssueID}
	slices.Sort(expected)
	p := canonicalOwnerIssueCreate{Plan: first.Plan, DependsOn: []string{expected[1], expected[0]}}
	envelope := h.env("issues.create", protocol.Target{Type: "issue"}, p, nil)
	created := h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "create"})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(created.Result, &issue); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(issue.DependsOn, expected) {
		t.Fatalf("response dependencies=%v want=%v", issue.DependsOn, expected)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		old, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), issue.IssueID, 1)
		if err != nil {
			return err
		}
		for _, v := range []protocol.Issue{current, old} {
			encoded, err := json.Marshal(v)
			if err != nil {
				return err
			}
			if !bytes.Equal(encoded, created.Result) {
				t.Fatal("response/current/history differ")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated := canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}
	updated.Plan.Title = "Clarified dependency goal"
	if op := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, updated, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	replay := h.exec(owner, envelope, CanonicalOwnerIssueCommand{Action: "create"})
	if replay.Status != protocol.OpSucceeded || !bytes.Equal(replay.Result, created.Result) {
		t.Fatal("create receipt changed after later revision", replay)
	}
}

func TestCanonicalPriorEvidenceRejectsForgedSnapshots(t *testing.T) {
	for _, group := range []string{"stage", "follow-on"} {
		for _, snapshot := range []string{"observation", "upgrade"} {
			t.Run(group+"/"+snapshot, func(t *testing.T) {
				h := newHarness(t)
				evidence := protocol.WorkItemEvidence{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Owner confirms the bounded result"}
				p := canonicalOwnerIssueCreate{Plan: protocol.WorkItemPlan{Title: "Retain verified evidence", Intent: "Preserve original proof", Project: "default", Stages: []protocol.WorkItemStage{}}}
				if group == "stage" {
					p.Plan.Stages = []protocol.WorkItemStage{{StageID: "report", Title: "Report", Status: protocol.WorkItemStageDone, Evidence: []protocol.WorkItemEvidence{evidence}}}
				} else {
					p.Plan.FollowOns = []protocol.WorkItemFollowOn{{FollowOnID: "answer", Kind: protocol.WorkItemFollowOnCommitment, Text: "Answer original question", Source: "Owner request", Status: protocol.WorkItemFollowOnResolved, Evidence: []protocol.WorkItemEvidence{evidence}}}
				}
				created := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
				if created.Status != protocol.OpSucceeded {
					t.Fatal(created)
				}
				var issue protocol.Issue
				if err := json.Unmarshal(created.Result, &issue); err != nil {
					t.Fatal(err)
				}
				update := canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}
				var original protocol.WorkItemEvidence
				if group == "stage" {
					original = issue.Plan.Stages[0].Evidence[0]
				} else {
					original = issue.Plan.FollowOns[0].Evidence[0]
				}
				forged := original
				if snapshot == "observation" {
					forged.Observation = &protocol.DeliveryObservation{Generation: 777}
				} else {
					forged.Upgrade = &protocol.UpgradeEvidence{Revision: 777}
				}
				// Retain the original item as well: checking only that it remains
				// present must not let a same-identity forged copy into either history.
				if group == "stage" {
					update.Plan.Stages[0].Evidence = append([]protocol.WorkItemEvidence{original}, forged)
				} else {
					update.Plan.FollowOns[0].Evidence = append([]protocol.WorkItemEvidence{original}, forged)
				}
				before := canonicalGuardBusinessSnapshot(t, h)
				rejected := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, update, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
				expectWorkCode(t, rejected, protocol.CodeInvalidRequest)
				if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
					t.Fatal("caller snapshot escaped into current/history")
				}
				// Keeping the exact frozen evidence remains a valid ordinary edit.
				if group == "stage" {
					update.Plan.Stages[0].Evidence = []protocol.WorkItemEvidence{original}
				} else {
					update.Plan.FollowOns[0].Evidence = []protocol.WorkItemEvidence{original}
				}
				update.Plan.Title = "Clarified title preserving frozen proof"
				if op := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, update, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			})
		}
	}
}

func TestCanonicalProvidedEvidenceRequiresSummaryBeforeHistory(t *testing.T) {
	for _, group := range []string{"stage", "follow-on"} {
		for _, summary := range []string{"", " \t"} {
			t.Run(group+"/"+summary, func(t *testing.T) {
				h := newHarness(t)
				p := canonicalOwnerIssueCreate{Plan: protocol.WorkItemPlan{Title: "Report", Project: "default", Intent: "Deliver report", Stages: []protocol.WorkItemStage{}}}
				evidence := []protocol.WorkItemEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: summary}}
				if group == "stage" {
					p.Plan.Stages = []protocol.WorkItemStage{{StageID: "report", Title: "Report", Status: protocol.WorkItemStagePlanned, Evidence: evidence}}
				} else {
					p.Plan.FollowOns = []protocol.WorkItemFollowOn{{FollowOnID: "report", Kind: protocol.WorkItemFollowOnCommitment, Text: "Report outcome", Source: "Owner request", Status: protocol.WorkItemFollowOnOpen, Evidence: evidence}}
				}
				before := canonicalGuardBusinessSnapshot(t, h)
				op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
				expectWorkCode(t, op, protocol.CodeInvalidRequest)
				if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
					t.Fatal("invalid evidence entered frozen history")
				}
				evidence[0].Summary = "Owner confirms the report"
				created := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
				if created.Status != protocol.OpSucceeded {
					t.Fatal(created)
				}
				var issue protocol.Issue
				if err := json.Unmarshal(created.Result, &issue); err != nil {
					t.Fatal(err)
				}
				if group == "stage" {
					issue.Plan.Stages[0].Status = protocol.WorkItemStageDone
				} else {
					issue.Plan.FollowOns[0].Status = protocol.WorkItemFollowOnResolved
				}
				updated := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
				if updated.Status != protocol.OpSucceeded {
					t.Fatal(updated)
				}
			})
		}
	}
}

func TestCanonicalOwnerCitesCrossIssueMaterialWithoutResponsibilityTransfer(t *testing.T) {
	for _, author := range []string{"owner", "main_agent"} {
		t.Run(author, func(t *testing.T) {
			h := newHarness(t)
			var op protocol.Operation
			if author == "owner" {
				legacy := h.createWork(workPlan())
				op = h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: "work-item:" + legacy.WorkItemID, Body: "An existing report in another issue"}, nil), SubmitMessage{})
				if op.Status != protocol.OpAccepted {
					t.Fatal(op)
				}
			} else {
				message, turn := startReviewTurn(t, h)
				op = h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "An assistant's investigative report", ReplyTo: message, TurnID: turn.TurnID, ItemID: "report"}, nil), RecordAgentMessage{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			var source struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(op.Result, &source); err != nil {
				t.Fatal(err)
			}
			bindings, associations, obligations := h.count("conversation_bindings"), h.count("message_work_items"), h.count("message_context_obligations")
			p := canonicalOwnerIssueCreate{canonicalOwnerIssueSource: canonicalOwnerIssueSource{SourceMessageID: source.MessageID, ExpectedSourceRevision: rev(1)}, Plan: protocol.WorkItemPlan{Title: "Use existing report", Project: "default", Intent: "Investigate a new goal", Stages: []protocol.WorkItemStage{}}}
			created := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
			if created.Status != protocol.OpSucceeded {
				t.Fatal(created)
			}
			var issue protocol.Issue
			if err := json.Unmarshal(created.Result, &issue); err != nil {
				t.Fatal(err)
			}
			if issue.ParentIssueID != "" {
				t.Fatal("material citation assigned a parent")
			}
			if h.count("conversation_bindings") != bindings || h.count("message_work_items") != associations || h.count("message_context_obligations") != obligations {
				t.Fatal("material citation transferred responsibility")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				message, err := store.GetMessage(t.Context(), tx.Q(), source.MessageID)
				if err != nil {
					return err
				}
				if message.Author != author {
					t.Fatalf("material author became %s", message.Author)
				}
				var ref, reason string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT source_message_id,reason_json FROM issue_control_revisions WHERE issue_id=? AND revision=1`, issue.IssueID).Scan(&ref, &reason); err != nil {
					return err
				}
				var saved canonicalOwnerIssueCreate
				if err := json.Unmarshal([]byte(reason), &saved); err != nil {
					return err
				}
				if ref != source.MessageID || saved.ExpectedSourceRevision == nil || *saved.ExpectedSourceRevision != 1 {
					t.Fatal("material reference lost its exact version")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
