package tools

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedPublicationLogsRawFailureWithoutExposingBody(t *testing.T) {
	f := newHistoricalHostedDelivery(t)
	var logs bytes.Buffer
	f.e.svc.log = slog.New(slog.NewJSONHandler(&logs, nil))
	if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER publication_storage_failure BEFORE INSERT ON messaging_messages WHEN NEW.direction='publication' BEGIN SELECT RAISE(ABORT,'injected publication storage failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := f.e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, f.issue.IssueID).Scan(&revision)
	}); err != nil {
		t.Fatal(err)
	}
	before := f.count(t, "messaging_messages")
	const privateBody = "Private report body must stay out of diagnostic errors"
	payload, _ := json.Marshal(protocol.CoordinationReply{ReplyTo: f.turn.MessageID, Body: privateBody, Purpose: "final", Disposition: "linked", WorkItemIDs: []string{f.issue.IssueID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: f.issue.IssueID, Revision: revision}}, ReviewedKinds: []string{"goal", "constraint", "authorization", "refusal", "commitment", "question", "suggestion"}})
	args, _ := json.Marshal(hostedMutationArgs{EffectSeq: 1, TargetID: f.turn.RequestID, Payload: payload})
	request := f.ids.New(protocol.PrefixRequest)
	result := f.e.svc.Handle(t.Context(), Call{Principal: f.principal, Native: "publication-storage-failure", RequestID: request, TurnID: f.turn.TurnID, ExecutionRequestID: f.turn.RequestID, Tool: "conversation_reply", Arguments: args})
	var answer answerView
	if err := json.Unmarshal([]byte(result.Text), &answer); err != nil || result.Success || answer.Error == nil || answer.Error.Code != protocol.CodeInternal {
		t.Fatal("storage failure was not returned as a failed tool", result, err)
	}
	for _, evidence := range []string{"injected publication storage failure", "conversation_reply", request, f.turn.RequestID} {
		if !strings.Contains(logs.String(), evidence) {
			t.Fatal("original publication failure lost its diagnostic identity", evidence, logs.String())
		}
	}
	if strings.Contains(result.Text, "injected publication") || strings.Contains(result.Text, privateBody) || strings.Contains(logs.String(), privateBody) {
		t.Fatal("publication diagnostics exposed internal details or the report body")
	}
	if count := f.count(t, "messaging_messages"); count != before {
		t.Fatalf("failed publication committed %d messages", count)
	}
}
