package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func linkIssueSteerSource(t *testing.T, h *harness, issue, source string) {
	t.Helper()
	var association protocol.MessageAssociations
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		association, err = store.MessageAssociations(t.Context(), tx.Q(), source)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := protocol.SetIssueMessageAssociationsPayload{
		Disposition: "linked", ContentRevision: association.ContentRevision, IssueIDs: []string{issue},
	}
	op := h.exec(owner, h.env("conversation.set_issue_associations", protocol.Target{Type: "message", ID: source}, p, rev(association.Revision)), SetIssueMessageAssociations{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func urgentTestEnvelope(t *testing.T, h *harness, issue, run, source, revokes string) *protocol.Envelope {
	t.Helper()
	i := canonicalTaskIssue(t, h, issue)
	payload := protocol.IssueOwnerSteerPayload{
		RunID: run, ExpectedAssignmentEpoch: i.AssignmentEpoch, SourceMessageID: source,
		ExpectedSourceRevision: 1, RevokesSourceMessageID: revokes,
	}
	return h.env(protocol.OpIssueOwnerSteer, protocol.Target{Type: "issue", ID: issue}, payload, rev(i.Revision))
}

func steerOperation(t *testing.T, op protocol.Operation, status string) protocol.IssueOwnerSteerView {
	t.Helper()
	var receipt protocol.IssueOwnerSteerView
	if op.Error != nil || json.Unmarshal(op.Result, &receipt) != nil || receipt.Status != status || receipt.MessageID == "" {
		t.Fatalf("expected receipt %s, got %+v, result %+v", status, op, receipt)
	}
	return receipt
}

func TestIssueOwnerSteerRevocationAndReceipt(t *testing.T) {
	ctx := t.Context()
	h, _, issue, as, _ := reviewManagerAssignment(t)
	turn, _ := startHostedIssueReview(t, h, issue)
	ordinary := queueMessage(t, h, "Observe ordinary queues")
	old := queueMessage(t, h, "Deploy this build")
	revocation := queueMessage(t, h, "Revoke permission to deploy this build")
	for _, source := range []string{ordinary, old, revocation} {
		linkIssueSteerSource(t, h, issue.IssueID, source)
	}
	send := func(source, revokes string) (*protocol.Envelope, protocol.IssueOwnerSteerView) {
		t.Helper()
		env := urgentTestEnvelope(t, h, issue.IssueID, as.RunID, source, revokes)
		return env, steerOperation(t, h.exec(owner, env, SteerIssueOwner{}), "queued")
	}
	_, pending := send(ordinary, "")
	_, earlier := send(old, "")
	env, receipt := send(revocation, old)

	read := func(id, want string) protocol.IssueOwnerSteerView {
		t.Helper()
		var receipt protocol.IssueOwnerSteerView
		if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
			activity, err := views.IssueActivity(ctx, tx.Q(), issue.IssueID, 0)
			if err != nil {
				return err
			}
			offset := slices.IndexFunc(activity.Items, func(entry protocol.IssueActivityEntry) bool {
				return entry.Block != nil && entry.Block.Trigger.MessageID == id
			})
			if offset < 0 {
				t.Fatalf("missing urgent activity for %s", id)
			}
			entry := activity.Items[offset]
			if entry.Author != "操作员 · "+owner.Subject || entry.Block.Trigger.IssueOwnerSteer == nil || entry.Block.Trigger.IssueOwnerSteer.Status != want {
				t.Fatalf("urgent activity author or receipt mismatch: %+v", entry)
			}
			public, err := views.OwnerIssueMessage(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			if public.Author == "owner" || public.IssueOwnerSteer == nil || public.IssueOwnerSteer.Status != want {
				t.Fatalf("public item should report %s with honest authorship, got %+v", want, public)
			}
			receipt = *public.IssueOwnerSteer
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	read(receipt.MessageID, "queued")
	claim := func(supported bool) IssueOwnerSteerDispatch {
		t.Helper()
		d, err := h.app.ClaimIssueOwnerSteer(ctx, turn.RequestID, as.RunID, turn.ThreadID, turn.TurnID, supported)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := claim(true); d.MessageID != receipt.MessageID || !strings.Contains(d.Text, revocation) || !strings.Contains(d.Text, old) || !strings.Contains(d.Text, "Revoke permission to deploy this build") {
		t.Fatalf("revocation did not preempt pending notices: %+v", d)
	} else {
		if err := h.app.RecordIssueOwnerSteerAnswer(ctx, receipt.MessageID, "accepted", "waiting_safe_boundary"); err != nil {
			t.Fatal(err)
		}
		if r := read(receipt.MessageID, "accepted"); r.NativeItemID != "" || r.AcceptedAt == 0 {
			t.Fatal(r)
		}
		for _, item := range []struct{ conversation, turn, text, id string }{
			{"foreign-conversation", turn.TurnID, d.Text, "foreign-conversation"},
			{as.Manager.ConversationID, "foreign-turn", d.Text, "foreign-turn"},
			{as.Manager.ConversationID, turn.TurnID, d.Text + "foreign", "foreign-input"},
			{as.Manager.ConversationID, turn.TurnID, d.Text, ""},
		} {
			if err := h.app.ConsumeIssueOwnerSteer(ctx, item.conversation, turn.ThreadID, item.turn, item.text, item.id); err != nil {
				t.Fatal(err)
			}
		}
		read(receipt.MessageID, "accepted")
		if err := h.app.ConsumeIssueOwnerSteer(ctx, as.Manager.ConversationID, turn.ThreadID, turn.TurnID, d.Text, "native-user-item"); err != nil {
			t.Fatal(err)
		}
	}
	if r := read(receipt.MessageID, "consumed"); r.NativeItemID != "native-user-item" || r.SourceMessageID != revocation || r.RunID != as.RunID || r.TaskID != as.TaskID {
		t.Fatal(r)
	}
	env.RequestID = h.ids.New(protocol.PrefixRequest)
	if replay := steerOperation(t, h.exec(owner, env, SteerIssueOwner{}), "consumed"); replay.MessageID != receipt.MessageID {
		t.Fatal(replay)
	}
	duplicate := urgentTestEnvelope(t, h, issue.IssueID, as.RunID, revocation, old)
	if got, err := h.app.Execute(ctx, owner, duplicate, SteerIssueOwner{}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal(got, err)
	}
	if d := claim(false); d.MessageID != "" {
		t.Fatal(d)
	}
	read(pending.MessageID, "unsupported")
	if d := claim(true); d.MessageID != "" || read(earlier.MessageID, "rejected").Reason != "source_revoked" {
		t.Fatal("old authority dispatched after revocation", d)
	}
	if h.count("issue_owner_steers") != 3 || h.count("runs") != 1 {
		t.Fatal("urgent notices created duplicate or replacement work")
	}
}

func TestIssueOwnerSteerAuthorizationAndUnknownRecovery(t *testing.T) {
	ctx := t.Context()
	h, _, issue, as, _ := reviewManagerAssignment(t)
	turn, _ := startHostedIssueReview(t, h, issue)
	original := queueMessage(t, h, "Authorized text from the owner")
	foreign := queueMessage(t, h, "Contents belong to another conversation")
	linkIssueSteerSource(t, h, issue.IssueID, original)
	if op, err := h.app.Execute(ctx, owner, urgentTestEnvelope(t, h, issue.IssueID, as.RunID, foreign, ""), SteerIssueOwner{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("foreign source was admitted", op, err)
	}
	env := urgentTestEnvelope(t, h, issue.IssueID, as.RunID, original, "")
	if op, err := h.app.Execute(ctx, runPrincipal(as.RunID, protocol.RoleIssueManager), env, SteerIssueOwner{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("manager was admitted as the sender", op, err)
	}
	receipt := steerOperation(t, h.exec(owner, env, SteerIssueOwner{}), "queued")
	d, err := h.app.ClaimIssueOwnerSteer(ctx, turn.RequestID, as.RunID, turn.ThreadID, turn.TurnID, true)
	if err != nil || d.MessageID != receipt.MessageID {
		t.Fatal(d, err)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	replay := func(status string) protocol.IssueOwnerSteerView {
		t.Helper()
		env.RequestID = h.ids.New(protocol.PrefixRequest)
		return steerOperation(t, h.exec(owner, env, SteerIssueOwner{}), status)
	}
	if r := replay("unknown"); r.Reason != "no_consumption_receipt" || r.NativeItemID != "" {
		t.Fatal(r)
	}
	if err := h.app.RecordIssueOwnerSteerAnswer(ctx, receipt.MessageID, "accepted", "waiting_safe_boundary"); err != nil {
		t.Fatal(err)
	}
	if r := replay("unknown"); r.Reason != "no_consumption_receipt" || r.AcceptedAt == 0 || r.NativeItemID != "" {
		t.Fatal("late acknowledgement must remain distinct from consumption after the turn ends", r)
	}
	if again, err := h.app.ClaimIssueOwnerSteer(ctx, turn.RequestID, as.RunID, turn.ThreadID, turn.TurnID, true); err != nil || again.MessageID != "" {
		t.Fatal("uncertain input was offered again", again, err)
	}
	if err := h.app.ConsumeIssueOwnerSteer(ctx, as.Manager.ConversationID, turn.ThreadID, turn.TurnID, d.Text, "retained-item"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RecordIssueOwnerSteerAnswer(ctx, receipt.MessageID, "accepted", "waiting_safe_boundary"); err != nil {
		t.Fatal(err)
	}
	if r := replay("consumed"); r.NativeItemID != "retained-item" || r.AcceptedAt == 0 {
		t.Fatal(r)
	}
	if page := readMessageQueue(t, h, "owner"); !slices.ContainsFunc(page.Items, func(entry protocol.ConversationQueueEntry) bool {
		return entry.Item.MessageID == original && entry.Status == "queued" && entry.CanWithdraw && entry.Item.ConsumedIssueOwnerSteer == nil
	}) {
		t.Fatal("a receipt for a linked Issue changed the original Main delivery", page)
	}
}

func TestIssueOwnerSteerRetainsDelegatedMainSender(t *testing.T) {
	ctx := t.Context()
	h, _, issue, as, _ := reviewManagerAssignment(t)
	inventoryCategory(t, h, "planner", 0, "active")
	accepted, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	if accepted.Status != protocol.OpSucceeded {
		t.Fatal(accepted)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared), WithMainOwner(owner.Subject))
	startHostedIssueReview(t, h, issue)
	delivery, _, ok, err := h.app.NextDelivery(ctx, MainThreadDestination)
	if err != nil || !ok {
		t.Fatal("original owner input was not admitted to main", ok, err)
	}
	main := startHostedReview(t, h, "main", delivery)
	source := main.MessageID
	foreign := queueMessage(t, h, "This owner input was not admitted to Main")
	for _, id := range []string{source, foreign} {
		linkIssueSteerSource(t, h, issue.IssueID, id)
	}
	send := func(id string, effect int64) (protocol.Operation, error) {
		t.Helper()
		env := urgentTestEnvelope(t, h, issue.IssueID, as.RunID, id, "")
		delegated := WithOwnerExecution(ctx, &OwnerExecution{
			Actor: mainAgent, ExecutionID: main.RequestID, CallID: env.RequestID, EffectSeq: effect,
		})
		return h.app.Execute(delegated, owner, env, SteerIssueOwner{})
	}
	if result, err := send(foreign, 1); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("Main routed a source it had not received", result, err)
	}
	result, err := send(source, 2)
	if err != nil {
		t.Fatal(err)
	}
	receipt := steerOperation(t, result, "queued")
	if receipt.Sender != mainAgent || receipt.SourceMessageID != source {
		t.Fatal("delegation replaced the actual sender or owner original", receipt)
	}
	if allowed, err := h.app.OwnerMessageAuthority(ctx, receipt.MessageID); err != nil || allowed {
		t.Fatal("routed notice gained independent owner authority", allowed, err)
	}
}

func TestIssueOwnerSteerRejectsMediaAndEmptyText(t *testing.T) {
	ctx := t.Context()
	h, _, issue, as, _ := reviewManagerAssignment(t)
	startHostedIssueReview(t, h, issue)
	uploaded, err := h.app.UploadConversationAttachment(ctx, owner, h.ids.New(protocol.PrefixRequest), "limits.txt", "text/plain", strings.NewReader("Release constraints"))
	if err != nil {
		t.Fatal(err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(uploaded.Result, &file); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		payload protocol.SubmitMessagePayload
	}{
		{"image only", protocol.SubmitMessagePayload{Images: []protocol.ConversationImageInput{testConversationImage(t)}}},
		{"text and attachment", protocol.SubmitMessagePayload{Body: "See the attached limits", Attachments: []string{file.FileID}}},
		{"blank text", protocol.SubmitMessagePayload{Body: "  \n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, tc.payload, nil), SubmitMessage{})
			if op.Status != protocol.OpAccepted {
				t.Fatal(op)
			}
			var message struct {
				ID string `json:"message_id"`
			}
			if err := json.Unmarshal(op.Result, &message); err != nil {
				t.Fatal(err)
			}
			linkIssueSteerSource(t, h, issue.IssueID, message.ID)
			env := urgentTestEnvelope(t, h, issue.IssueID, as.RunID, message.ID, "")
			if op, err := h.app.Execute(ctx, owner, env, SteerIssueOwner{}); err != nil || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
				t.Fatal("partial urgent content was accepted", op, err)
			}
		})
	}
	if h.count("issue_owner_steers") != 0 || h.count("manager_native_requests") != 0 {
		t.Fatal("unsupported source produced an urgent queue entry")
	}
}

func TestIssueOwnerSteerFormalReplyRequiresWitnessedTurn(t *testing.T) {
	for _, sender := range []string{"operator", "main"} {
		t.Run(sender, func(t *testing.T) {
			ctx := t.Context()
			h, _, issue, assignment, source := reviewManagerAssignment(t)
			if sender == "main" {
				inventoryCategory(t, h, "planner", 0, "active")
				accepted, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
				if accepted.Status != protocol.OpSucceeded {
					t.Fatal(accepted)
				}
				h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared), WithMainOwner(owner.Subject))
			}
			turn, _ := startHostedIssueReview(t, h, issue)
			senderContext := ctx
			if sender == "main" {
				delivery, _, ok, err := h.app.NextDelivery(ctx, MainThreadDestination)
				if err != nil || !ok {
					t.Fatal("owner input not admitted to Main", ok, err)
				}
				main := startHostedReview(t, h, "main", delivery)
				source = main.MessageID
				senderContext = WithOwnerExecution(ctx, &OwnerExecution{
					Actor: mainAgent, ExecutionID: main.RequestID, CallID: "route-urgent", EffectSeq: 1,
				})
			}
			linkIssueSteerSource(t, h, issue.IssueID, source)
			env := urgentTestEnvelope(t, h, issue.IssueID, assignment.RunID, source, "")
			op, err := h.app.Execute(senderContext, owner, env, SteerIssueOwner{})
			if err != nil {
				t.Fatal(err)
			}
			notice := steerOperation(t, op, "queued")

			if sender == "main" {
				// The owner can ask a new urgent question about grouped work.
				// Acknowledging its dispatch alone still cannot open a reply.
				agreement := queueMessage(t, h, "Report the two Issues together when complete.")
				other := createCanonicalOwnerTestIssue(t, h)
				ids := []string{issue.IssueID, other.IssueID}
				setReportForTest(t, h, agreement, ids, ids)
			}
			conversation := assignment.Manager.ConversationID
			principal := protocol.Principal{Kind: protocol.PrincipalRun, Subject: assignment.RunID,
				Role: protocol.RoleIssueManager, Thread: turn.ThreadID, ConversationID: conversation}
			publish := func(effect int64) (protocol.Operation, error) {
				t.Helper()
				var command Command = PublishCommunication{}
				var payload any = protocol.MessagingPublication{ReplyTo: notice.MessageID, Body: "The urgent input was read."}
				if sender == "main" {
					command = CoordinationReply{}
					payload = protocol.CoordinationReply{ReplyTo: notice.MessageID, Body: "The urgent input was read."}
				}
				env := h.env(command.Operation(), protocol.Target{Type: "logical_turn", ID: turn.RequestID}, payload, nil)
				return h.app.Execute(ctx, principal, env, HostedCoordinationCommand{Command: command, ExecutionID: turn.RequestID, EffectSeq: effect})
			}
			requireForbidden := func(op protocol.Operation, err error) {
				t.Helper()
				if protocol.CodeOf(err) != protocol.CodeForbidden && (op.Error == nil || op.Error.Code != protocol.CodeForbidden) {
					t.Fatal("input outside the witnessed turn was allowed", op, err)
				}
			}
			before := h.count("messaging_messages")
			requireForbidden(publish(1))
			dispatch, err := h.app.ClaimIssueOwnerSteer(ctx, turn.RequestID, assignment.RunID, turn.ThreadID, turn.TurnID, true)
			if err != nil || dispatch.MessageID != notice.MessageID {
				t.Fatal(dispatch, err)
			}
			if err := h.app.RecordIssueOwnerSteerAnswer(ctx, notice.MessageID, "accepted", "waiting_safe_boundary"); err != nil {
				t.Fatal(err)
			}
			requireForbidden(publish(2))
			if h.count("messaging_messages") != before {
				t.Fatal("an unconsumed urgent input created a fallback publication")
			}
			if err := h.app.ConsumeIssueOwnerSteer(ctx, conversation, turn.ThreadID, turn.TurnID, dispatch.Text, "witnessed-user-item"); err != nil {
				t.Fatal(err)
			}
			op, err = publish(3)
			if err != nil || op.Error != nil {
				t.Fatal(op, err)
			}
			replyID := messageID(t, op)
			published, err := h.app.MessagingMessage(ctx, replyID)
			if err != nil || published.Direction != "publication" || published.ChannelID != "web" || published.ReplyTo != notice.MessageID {
				t.Fatal("no exact Web reply to the urgent notice", published, err)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				requireForbidden(protocol.Operation{}, communicationSourceAllowed(ctx, tx.Q(), turn.RequestID, "main", notice.MessageID))
				requireForbidden(protocol.Operation{}, communicationSourceAllowed(ctx, tx.Q(), turn.RequestID+"-other", conversation, notice.MessageID))
				activity, err := views.IssueActivity(ctx, tx.Q(), issue.IssueID, 0)
				if err != nil {
					return err
				}
				for _, entry := range activity.Items {
					if entry.Block == nil || entry.Block.Trigger.MessageID != notice.MessageID {
						continue
					}
					if entry.Block.Trigger.IssueOwnerSteer == nil || entry.Block.Trigger.IssueOwnerSteer.Status != "consumed" {
						t.Fatal("the activity lost its consumption receipt", entry)
					}
					for _, item := range entry.Block.Items {
						if item.MessageID == replyID && item.ReplyTo == notice.MessageID {
							return nil
						}
					}
					t.Fatal("the page did not project the real reply under its urgent source", entry)
				}
				t.Fatal("the page did not project the urgent source")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			turn.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(ctx, turn); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
				requireForbidden(protocol.Operation{}, communicationSourceAllowed(ctx, tx.Q(), turn.RequestID, conversation, notice.MessageID))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
