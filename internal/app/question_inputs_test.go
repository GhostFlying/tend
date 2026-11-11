package app

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func questionInputFixture(t *testing.T) (*harness, protocol.Assignment, protocol.WorkerQuestion, protocol.QuestionInputPayload, protocol.ConversationAttachment, protocol.ConversationAttachment) {
	t.Helper()
	h, as, _ := questionRun(t, protocol.RoleImplement)
	q := askQuestion(t, h, as, scopeQuestion)
	if op := answerQuestion(t, h, q, "", "Use the supplemental evidence"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	q, err := h.app.WorkerQuestion(t.Context(), runPrincipal(as.RunID, as.Role), q.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	file := uploadChatFile(t, h, "evidence.bin", "application/octet-stream", []byte{0, 255, 17, 0})
	other := uploadChatFile(t, h, "private.txt", "text/plain", []byte("unselected original"))
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Files for the original question", ReplyTo: q.QuestionMessageID, Attachments: []string{file.FileID, other.FileID}}, nil), SubmitMessage{})
	var result struct {
		MessageID string `json:"message_id"`
	}
	if (op.Status != protocol.OpSucceeded && op.Status != protocol.OpAccepted) || json.Unmarshal(op.Result, &result) != nil {
		t.Fatal(op)
	}
	p := protocol.QuestionInputPayload{Version: q.Version, RunID: as.RunID, LeaseID: as.LeaseID, IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: result.MessageID, ExpectedSourceRevision: rev(1)}, FileIDs: []string{file.FileID}, Reason: "Owner supplied this exact original as answer evidence"}
	return h, as, q, p, file, other
}

func TestQuestionInputsOriginalAnswerMetadataBytesAndRevocation(t *testing.T) {
	h, as, q, p, file, other := questionInputFixture(t)
	principal := runPrincipal(as.RunID, as.Role)
	initial, err := h.app.QuestionInputs(t.Context(), q.DecisionID)
	if err != nil || initial.Revision != 0 || initial.QuestionVersion != q.Version || initial.Original == nil || initial.Original.RunID != as.RunID || initial.Original.LeaseID != as.LeaseID {
		t.Fatal("cannot discover original input scope", initial, err)
	}
	beforeTask, beforeRun, beforeLease := h.task(as.TaskID), h.run(as.RunID), h.lease(as.RunID)
	beforeSpec := h.storedSpec(as.TaskID, as.SpecVersion)
	if _, body, err := h.app.OpenRunFile(t.Context(), principal, file.FileID); err == nil {
		body.Close()
		t.Fatal("answer attachment was implicitly authorized")
	}
	env := h.env("decisions.grant_inputs", protocol.Target{Type: "decision", ID: q.DecisionID}, p, rev(0))
	op := h.exec(owner, env, QuestionInputCommand{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	replay := h.exec(owner, env, QuestionInputCommand{})
	if replay.Error == nil || replay.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("stale grant was admitted", replay)
	}
	saved, err := h.app.WorkerQuestion(t.Context(), principal, q.DecisionID)
	if err != nil || saved.InputRevision != 1 || len(saved.Inputs) != 1 || saved.Inputs[0].FileID != file.FileID || saved.Inputs[0].SHA256 != file.SHA256 || !bytes.Equal(saved.Answer, q.Answer) {
		t.Fatalf("metadata/answer %+v %v", saved, err)
	}
	metadata, body, err := h.app.OpenRunFile(t.Context(), principal, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(body)
	body.Close()
	if err != nil || !bytes.Equal(raw, []byte{0, 255, 17, 0}) || metadata.SHA256 != file.SHA256 {
		t.Fatal(metadata, raw, err)
	}
	if _, body, err := h.app.OpenRunFile(t.Context(), principal, other.FileID); err == nil {
		body.Close()
		t.Fatal("other attachment authorized")
	}
	if _, body, err := h.app.OpenRunFile(t.Context(), runPrincipal(as.RunID, protocol.RoleAccept), file.FileID); err == nil {
		body.Close()
		t.Fatal("other role authorized")
	}
	var seq int
	var name, path string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `PRAGMA database_list`).Scan(&seq, &name, &path)
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), path, h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	h.store = reopened
	h.app = New(reopened, h.ids, h.clock.Now)
	if _, body, err := h.app.OpenRunFile(t.Context(), principal, file.FileID); err != nil {
		t.Fatal("grant lost after DB reopen", err)
	} else {
		body.Close()
	}
	// A grant is an appended input fact, never a task spec or execution change.
	if h.task(as.TaskID) != beforeTask || h.run(as.RunID) != beforeRun || h.lease(as.RunID) != beforeLease || string(mustJSON(t, h.storedSpec(as.TaskID, as.SpecVersion))) != string(mustJSON(t, beforeSpec)) {
		t.Fatal("grant changed original execution")
	}
	r := protocol.QuestionInputPayload{GrantRevision: 1, Reason: "Owner revokes future fetches"}
	revoke := h.exec(owner, h.env("decisions.revoke_inputs", protocol.Target{Type: "decision", ID: q.DecisionID}, r, rev(1)), QuestionInputCommand{Revoke: true})
	if revoke.Status != protocol.OpSucceeded {
		t.Fatal(revoke)
	}
	if _, body, err := h.app.OpenRunFile(t.Context(), principal, file.FileID); err == nil {
		body.Close()
		t.Fatal("revoked grant still allowed fetch")
	}
	replay = h.exec(owner, env, QuestionInputCommand{})
	if replay.Error == nil || replay.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("revoked authority was restored by an old command", replay)
	}
	saved, err = h.app.WorkerQuestion(t.Context(), principal, q.DecisionID)
	if err != nil || len(saved.Inputs) != 0 || saved.InputRevision != 2 || !bytes.Equal(saved.Answer, q.Answer) {
		t.Fatal(saved, err)
	}
}

func TestQuestionInputsRejectRebindingSourceAndStaleCAS(t *testing.T) {
	for _, mode := range []string{"run", "lease", "version", "source", "source_revision", "file", "duplicate", "revision", "new_spec", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			h, as, q, p, file, _ := questionInputFixture(t)
			revision := int64(0)
			switch mode {
			case "run":
				p.RunID = h.ids.New(protocol.PrefixRun)
			case "lease":
				p.LeaseID = h.ids.New(protocol.PrefixLease)
			case "version":
				p.Version++
			case "source":
				p.SourceMessageID = q.QuestionMessageID
			case "source_revision":
				p.ExpectedSourceRevision = rev(2)
			case "file":
				p.FileIDs = []string{h.ids.New(protocol.PrefixFile)}
			case "duplicate":
				p.FileIDs = []string{file.FileID, file.FileID}
			case "revision":
				revision = 1
			case "new_spec":
				spec := testSpec
				spec.Goal = "new contract"
				op := h.exec(owner, h.env("tasks.update_intent", taskTarget(as.TaskID), updateIntentPayload{Spec: &spec}, rev(h.task(as.TaskID).ControlRevision)), UpdateIntent{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "cancelled":
				op := h.exec(owner, h.env("tasks.cancel", taskTarget(as.TaskID), nil, rev(h.task(as.TaskID).ControlRevision)), CancelTask{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			op, err := h.app.Execute(t.Context(), owner, h.env("decisions.grant_inputs", protocol.Target{Type: "decision", ID: q.DecisionID}, p, rev(revision)), QuestionInputCommand{})
			if err == nil && op.Status == protocol.OpSucceeded {
				t.Fatalf("invalid %s authorized", mode)
			}
			if h.count("events WHERE object_type='question_inputs'") != 0 {
				t.Fatal("rejected grant appended facts")
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestQuestionInputsMainRelayRequiresRealAdmission(t *testing.T) {
	h, _, q, p, _, _ := questionInputFixture(t)
	if op, err := h.app.Execute(t.Context(), mainAgent, h.env("decisions.grant_inputs", protocol.Target{Type: "decision", ID: q.DecisionID}, p, rev(0)), QuestionInputCommand{}); err == nil && op.Status == protocol.OpSucceeded {
		t.Fatal("unadmitted main granted file")
	}
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, configuration := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(configuration))
	// The fixture has earlier independent notification deliveries. Only this
	// actual owner reply is selected for the new admitted native execution.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE destination=? AND COALESCE(json_extract(payload_json,'$.message_id'),'')<>?`, MainThreadDestination, p.SourceMessageID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := startHostedReview(t, h, "main", delivery)
	command := HostedCoordinationCommand{Command: QuestionInputCommand{}, ExecutionID: turn.RequestID, EffectSeq: 1}
	op, err := h.app.Execute(t.Context(), mainAgent, h.env(command.Operation(), protocol.Target{Type: "decision", ID: q.DecisionID}, p, rev(0)), command)
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("admitted original owner reply rejected", op, err)
	}
	finishHostedReview(t, h, turn)
	_, nextDelivery := h.say("Revoke future access to the original question input")
	current := startHostedReview(t, h, "main", nextDelivery)
	associations := h.associations(p.SourceMessageID)
	classified := hostedEffect(t, h, mainAgent, current, 1, protocol.Target{Type: "message", ID: p.SourceMessageID}, rev(associations.Revision), protocol.SetIssueMessageAssociationsPayload{Disposition: "unassigned", ContentRevision: associations.ContentRevision}, SetIssueMessageAssociations{})
	if classified.Status != protocol.OpSucceeded {
		t.Fatal(classified)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var member bool
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE e.request_id=? AND m.message_id=?)`, current.RequestID, p.SourceMessageID).Scan(&member); err != nil {
			return err
		}
		if !member {
			t.Fatal("fixture did not classify historical reply")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for n, revoke := range []bool{false, true} {
		stale := p
		if revoke {
			stale.FileIDs = nil
			stale.GrantRevision = 1
		}
		cmd := QuestionInputCommand{Revoke: revoke}
		op, err := h.app.Execute(t.Context(), mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "decision", ID: q.DecisionID}, stale, rev(1)), HostedCoordinationCommand{Command: cmd, ExecutionID: current.RequestID, EffectSeq: int64(n + 2)})
		if err == nil && op.Status == protocol.OpSucceeded {
			t.Fatal("historical-only source authorized new input control", revoke)
		}
	}
	revoke := protocol.QuestionInputPayload{GrantRevision: 1, Reason: "Current owner revokes future reads", IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: current.MessageID, ExpectedSourceRevision: rev(1)}}
	op, err = h.app.Execute(t.Context(), mainAgent, h.env("decisions.revoke_inputs", protocol.Target{Type: "decision", ID: q.DecisionID}, revoke, rev(1)), HostedCoordinationCommand{Command: QuestionInputCommand{Revoke: true}, ExecutionID: current.RequestID, EffectSeq: 4})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("current owner revoke rejected", op, err)
	}
}
