package app

import (
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestOrdinaryPublicationOriginalWithoutArtifactAndOptionalMarker(t *testing.T) {
	for _, purpose := range []string{"", "progress", "final"} {
		t.Run(purpose, func(t *testing.T) {
			h, issue, producer, _, _ := ownerBoundDeliveryFixture(t, true)
			file, _ := h.uploadResultFile(producer, "direct.txt", []byte("immutable ordinary original"))
			if purpose == "progress" {
				// First publication in main must not replace the producing Issue's
				// ordinary upload scope or force it through artifact registration.
				p := protocol.MessagingPublication{Body: "Also visible in main", Attachments: []string{file.FileID}}
				if op := h.exec(owner, h.env("messaging.notify", protocol.Target{Type: "message"}, p, nil), MessagingCommand{Action: "notify"}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			task := h.task(producer.TaskID)
			duty := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
			artifacts, acceptances, runs := h.count("artifacts"), h.count("acceptances"), h.count("runs")
			delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+producer.Manager.ConversationID)
			if err != nil || !ok {
				t.Fatal(err)
			}
			turn := startHostedReview(t, h, producer.Manager.ConversationID, delivery)
			principal := runPrincipal(producer.RunID, producer.Role)
			principal.Thread, principal.ConversationID = turn.ThreadID, producer.Manager.ConversationID
			// No report/submit_result. An attachment-only message is ordinary output.
			p := protocol.CoordinationReply{Attachments: []string{file.FileID}, Purpose: purpose}
			env := h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, p, nil)
			command := HostedCoordinationCommand{Command: CoordinationReply{}, ExecutionID: turn.RequestID, EffectSeq: 1}
			op := h.exec(principal, env, command)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			replay := h.exec(principal, env, command)
			if replay.Error == nil || replay.Error.Code != protocol.CodeDuplicateEffect {
				t.Fatal("the original effect was repeated", replay)
			}
			var publications int
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM conversation_messages WHERE request_id=?`, env.RequestID).Scan(&publications)
			}); err != nil || publications != 1 {
				t.Fatal("lost reply created another message", publications, err)
			}
			var result struct {
				MessageID string `json:"message_id"`
			}
			_ = json.Unmarshal(op.Result, &result)
			message, err := h.app.MessagingMessage(t.Context(), result.MessageID)
			if err != nil || message.Purpose != purpose || len(message.Attachments) != 1 {
				t.Fatal(message, err)
			}
			meta, body, err := h.app.OpenConversationAttachment(t.Context(), file.FileID)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(body)
			_ = body.Close()
			if err != nil || string(data) != "immutable ordinary original" || meta.SHA256 != file.SHA256 {
				t.Fatal(meta, string(data), err)
			}
			if !reflect.DeepEqual(task, h.task(producer.TaskID)) || !reflect.DeepEqual(duty, deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)) || artifacts != h.count("artifacts") || acceptances != h.count("acceptances") || runs != h.count("runs") {
				t.Fatal("ordinary publication changed business state")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				_, _, err := store.GetFinalizedDeliverableFile(t.Context(), tx.Q(), file.FileID)
				if err == nil {
					t.Fatal("message forged formal artifact provenance")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOrdinaryPublicationRejectsAnotherIssueUpload(t *testing.T) {
	h, issue, producer, _, _ := ownerBoundDeliveryFixture(t, true)
	other := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, other)
	foreign, ok, _ := h.claimDeliverableRun()
	if !ok || foreign.TaskID != task {
		t.Fatal("foreign source fixture", foreign)
	}
	file, _ := h.uploadResultFile(foreign, "foreign.txt", []byte("outside original responsibility"))
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+producer.Manager.ConversationID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	turn := startHostedReview(t, h, producer.Manager.ConversationID, delivery)
	principal := runPrincipal(producer.RunID, producer.Role)
	principal.Thread, principal.ConversationID = turn.ThreadID, producer.Manager.ConversationID
	before := h.count("conversation_attachments")
	env := h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, protocol.CoordinationReply{Attachments: []string{file.FileID}}, nil)
	op, err := h.app.Execute(t.Context(), principal, env, HostedCoordinationCommand{Command: CoordinationReply{}, ExecutionID: turn.RequestID, EffectSeq: 1})
	if err == nil {
		err = op.Error
	}
	if protocol.CodeOf(err) != protocol.CodeForbidden || h.count("conversation_attachments") != before {
		t.Fatal("ordinary publication changed file scope", issue.IssueID, op)
	}
}
