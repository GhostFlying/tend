package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func canonicalGuardBusinessSnapshot(t *testing.T, h *harness) []byte {
	t.Helper()
	tables := []string{"work_items", "work_item_revisions", "issue_controls", "issue_control_revisions", "work_item_contexts", "work_item_context_facts", "message_context_obligations", "logical_turn_contexts", "logical_turn_messages", "message_dispositions", "message_work_items", "message_association_revisions", "message_association_history", "message_disposition_history", "conversation_bindings", "plan_commits", "agent_conversations", "conversation_messages", "work_item_task_links", "task_work_item_owners", "tasks", "task_specs", "work_item_choices", "followup_rules", "followup_revisions", "followup_occurrences", "followup_targets", "followup_registrations", "events", "outbox"}
	state := map[string][][]any{}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, table := range tables {
			// All identifiers are constant table names, never payload values.
			rows, err := tx.Q().QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY rowid")
			if err != nil {
				return err
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				return err
			}
			for rows.Next() {
				row := make([]any, len(columns))
				dest := make([]any, len(columns))
				for i := range row {
					dest[i] = &row[i]
				}
				if err := rows.Scan(dest...); err != nil {
					rows.Close()
					return err
				}
				for i, value := range row {
					if b, ok := value.([]byte); ok {
						row[i] = string(b)
					}
				}
				state[table] = append(state[table], row)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func createCanonicalGuardIssue(t *testing.T, h *harness, project string) protocol.Issue {
	t.Helper()
	p := canonicalOwnerIssueCreate{Plan: protocol.WorkItemPlan{Title: "Independent canonical responsibility", Intent: "Obtain an accepted result", Project: project, Stages: []protocol.WorkItemStage{}}}
	op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	return issue
}

func addLegacyGuardFact(t *testing.T, h *harness, item, request string, fact protocol.ContextFact) protocol.Operation {
	t.Helper()
	c := h.agentContext(item)
	envelope := h.env("work_item_context.fact", workTarget(item), fact, rev(c.Revision))
	envelope.ParentRequestID = request
	return h.exec(mainAgent, envelope, ContextCommand{Action: "fact"})
}

func TestCanonicalGuardRejectsOnlyAffectedLegacyFactScopes(t *testing.T) {
	for _, tc := range []struct {
		name, scope, project, canonicalProject string
		deny                                   bool
	}{
		{name: "global", scope: "global", canonicalProject: "video", deny: true},
		{name: "same project", scope: "project", project: "tend", canonicalProject: "tend", deny: true},
		{name: "different project", scope: "project", project: "tend", canonicalProject: "video"},
		{name: "local legacy", scope: "work_item", canonicalProject: "tend"},
		{name: "global without canonical", scope: "global"},
		{name: "project without canonical", scope: "project", project: "tend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.syncConfig(withProjects())
			plan := workPlan()
			plan.Project = "tend"
			legacy := h.createWork(plan)
			if tc.canonicalProject != "" {
				createCanonicalGuardIssue(t, h, tc.canonicalProject)
			}
			source, turn := startReviewTurn(t, h)
			// Classify using a real owner request. The source remains legacy only,
			// so an allowed case cannot accidentally attach canonical context.
			if op := h.associate(owner, source, legacy.WorkItemID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			fact := protocol.ContextFact{Kind: "constraint", Body: "Retain the original source", Scope: tc.scope, Project: tc.project, SourceMessageID: source}
			before := canonicalGuardBusinessSnapshot(t, h)
			op := addLegacyGuardFact(t, h, legacy.WorkItemID, turn.RequestID, fact)
			if tc.deny {
				expectWorkCode(t, op, protocol.CodeInvalidTransition)
				if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
					t.Fatal("scope rejection changed business state")
				}
				// app.Execute may durably retain its normal failed receipt; it
				// must not retain the legacy context attachment made beforehand.
			} else if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		})
	}
}

func TestCanonicalGuardSharedFactPrepareUsesPersistedImpact(t *testing.T) {
	for _, action := range []string{"confirm", "retire", "confirm already reviewed"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			legacy := h.createWork(workPlan())
			source, turn := startReviewTurn(t, h)
			if op := h.associate(owner, source, legacy.WorkItemID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			fact := protocol.ContextFact{Kind: "constraint", Body: "Shared rule predating canonical responsibility", Scope: "global", SourceMessageID: source}
			if op := addLegacyGuardFact(t, h, legacy.WorkItemID, turn.RequestID, fact); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var factID string
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT fact_id FROM work_item_context_facts WHERE source_message_id=?`, source).Scan(&factID)
			}); err != nil {
				t.Fatal(err)
			}
			// This is a fresh product create, never adoption of the legacy row.
			createCanonicalGuardIssue(t, h, "default")
			if action != "confirm already reviewed" {
				// Fault injection models the existing content-correction trigger
				// invalidating a shared fact, not a fabricated owner/agent source.
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE conversation_messages SET body=body || ' corrected' WHERE message_id=?`, source)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			c := h.agentContext(legacy.WorkItemID)
			payload := protocol.PrepareWorkItemContextPayload{Coverage: "Reviewed the exact retained source", Gaps: "No additional legacy gaps"}
			if action == "retire" {
				payload.RetiredFactIDs = []string{factID}
			} else {
				payload.ConfirmedFactIDs = []string{factID}
			}
			envelope := h.env("work_item_context.prepare", workTarget(legacy.WorkItemID), payload, rev(c.Revision))
			envelope.ParentRequestID = turn.RequestID
			before := canonicalGuardBusinessSnapshot(t, h)
			op := h.exec(mainAgent, envelope, ContextCommand{Action: "prepare"})
			if action == "confirm already reviewed" {
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				return
			}
			expectWorkCode(t, op, protocol.CodeInvalidTransition)
			if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
				t.Fatal("shared fact prepare rejection changed canonical or legacy state")
			}
		})
	}
}

func TestCanonicalGuardSourceAttachmentMatchesActualInsert(t *testing.T) {
	for _, tc := range []struct {
		name                                        string
		completed, alreadyAttached, canonicalSource bool
		deny                                        bool
	}{
		{name: "new canonical obligation", canonicalSource: true, deny: true},
		{name: "already attached", alreadyAttached: true, canonicalSource: true},
		{name: "completed responsibility", completed: true, canonicalSource: true},
		{name: "unrelated legacy source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalGuardIssue(t, h, "default")
			legacy := h.createWork(workPlan())
			old, first := startReviewTurn(t, h)
			// Classify and commit the original before the current turn begins.
			if op := h.associate(owner, old, legacy.WorkItemID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			h.prepareAgent(legacy.WorkItemID)
			commitFixtureTurn(t, h, first.RequestID)
			first.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, first.RequestID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			current, delivery := h.say("Review another source")
			second := store.MainTurn{RequestID: delivery.RequestID, MessageID: current, ThreadID: mainAgent.Thread, TurnID: "native-2", Status: store.TurnStarted}
			if err := h.app.PutMainTurn(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			// Precise low-level fixture only for trigger-effect inspection: the
			// authority and canonical issue were created by real owner commands.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if tc.canonicalSource {
					if _, err := tx.Exec(t.Context(), `INSERT INTO message_context_obligations(message_id,work_item_id,needs_review) VALUES(?,?,1) ON CONFLICT(message_id,work_item_id) DO UPDATE SET needs_review=1`, old, issue.IssueID); err != nil {
						return err
					}
				}
				if tc.alreadyAttached {
					if _, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_messages(logical_turn_id,message_id) VALUES(?,?)`, second.RequestID, old); err != nil {
						return err
					}
				}
				if tc.completed {
					if _, err := tx.Exec(t.Context(), `UPDATE logical_turns SET status='completed' WHERE logical_turn_id=?`, second.RequestID); err != nil {
						return err
					}
				}
				// This intentionally calls only the pre-write predicate: exact
				// trigger/no-op matrices supplement the actual command tests.
				x := &Exec{Env: &protocol.Envelope{ParentRequestID: second.RequestID}}
				err := rejectCanonicalLegacySourceAttachment(t.Context(), x, tx.Q(), old)
				if tc.deny {
					if protocol.CodeOf(err) != protocol.CodeInvalidTransition {
						return fmt.Errorf("expected canonical source rejection, got %v", err)
					}
					return nil
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCanonicalAssociationAttachmentKeepsRemovedSideResponsibility(t *testing.T) {
	for _, mode := range []string{"legacy plan", "legacy noop", "unassigned noop", "changed classification", "owner correction", "already attached", "completed logical"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			legacy := h.createWork(workPlan())
			old, first := startReviewTurn(t, h)
			if op := h.associate(mainAgent, old, legacy.WorkItemID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			h.prepareAgent(legacy.WorkItemID)
			commitFixtureTurn(t, h, first.RequestID)
			first.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, first.RequestID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			current, delivery := h.say("A separate active owner exchange")
			second := store.MainTurn{RequestID: delivery.RequestID, MessageID: current, ThreadID: mainAgent.Thread, TurnID: "native-2", Status: store.TurnStarted}
			if err := h.app.PutMainTurn(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			if op := h.associate(owner, old, issue.IssueID, legacy.WorkItemID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			desired := []string{legacy.WorkItemID}
			if mode == "unassigned noop" {
				desired = nil
			}
			// The real owner removal leaves a canonical needs_review tombstone,
			// even though subsequent reads expose only active legacy links.
			if op := h.associate(owner, old, desired...); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if mode == "changed classification" {
				// Fault-only unlocked legacy classification exercises the second
				// attachment branch. The real owner-removal no-op is tested above.
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					if _, err := tx.Exec(t.Context(), `UPDATE message_dispositions SET owner_locked=0 WHERE message_id=?`, old); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `UPDATE message_work_items SET owner_locked=0 WHERE message_id=? AND active=1`, old)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				desired = nil
			}
			if mode == "already attached" {
				// Existing membership has already fired0046. Its repeated read
				// inserts nothing and must not be rejected as a new attachment.
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_messages(logical_turn_id,message_id) VALUES(?,?)`, second.RequestID, old)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "completed logical" {
				if op := h.associate(mainAgent, current); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				commitFixtureTurn(t, h, second.RequestID)
				second.Status = store.TurnCompleted
				if err := h.app.PutMainTurn(t.Context(), second); err != nil {
					t.Fatal(err)
				}
			}
			associations := h.associations(old)
			p := protocol.SetMessageAssociationsPayload{Disposition: dispositionForTest(desired), WorkItemIDs: desired, ContentRevision: associations.ContentRevision}
			env := h.env("conversation.set_associations", protocol.Target{Type: "message", ID: old}, p, rev(associations.Revision))
			principal := mainAgent
			if mode == "owner correction" {
				principal = owner
			} else {
				env.ParentRequestID = second.RequestID
			}
			var command Command = SetMessageAssociations{}
			if mode == "legacy plan" {
				plan := legacy.WorkItemPlan
				plan.Title = "Revised legacy plan"
				env = h.env("work_items.update", workTarget(legacy.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: plan, SourceMessageID: old, CommitSeq: 99}, rev(legacy.Revision))
				env.ParentRequestID = second.RequestID
				command = UpdateIssue{}
			}
			before := canonicalGuardBusinessSnapshot(t, h)
			op := h.exec(principal, env, command)
			deny := mode == "legacy plan" || mode == "legacy noop" || mode == "unassigned noop" || mode == "changed classification"
			if deny {
				expectWorkCode(t, op, protocol.CodeInvalidTransition)
				if op.Error.Message != "legacy source review attaches canonical responsibility" {
					t.Fatalf("wrong boundary rejected request: %+v", op)
				}
				if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
					t.Fatal("association guard leaked source/context/history changes")
				}
			} else if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var pending int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE message_id=? AND work_item_id=?`, old, issue.IssueID).Scan(&pending); err != nil {
					return err
				}
				if pending != 1 {
					t.Fatal("removed-side responsibility was cleared")
				}
				var attached int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM logical_turn_contexts WHERE logical_turn_id=? AND work_item_id=?`, second.RequestID, issue.IssueID).Scan(&attached); err != nil {
					return err
				}
				want := 0
				if mode == "already attached" {
					want = 1
				}
				if attached != want {
					t.Fatalf("new main responsibility=%d want=%d", attached, want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
