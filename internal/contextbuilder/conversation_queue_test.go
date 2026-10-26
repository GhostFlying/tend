package contextbuilder

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestWithdrawnOwnerNeverAppearsInContextPacket(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	raw, _ := json.Marshal(protocol.SubmitMessagePayload{Body: "WITHDRAWN_INSTRUCTION_MARKER"})
	op, err := e.app.Execute(ctx, owner, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: "conversation.submit_message", Target: protocol.Target{Type: "conversation"}, Payload: raw}, app.SubmitMessage{})
	if err != nil || op.Status != protocol.OpAccepted {
		t.Fatalf("submit: %+v %v", op, err)
	}
	var submitted struct {
		ID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &submitted); err != nil {
		t.Fatal(err)
	}
	var control protocol.MessageControl
	if err := e.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		control, err = store.MessageControl(ctx, tx.Q(), submitted.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.exec(owner, "conversation.withdraw_message", protocol.Target{Type: "message", ID: submitted.ID}, &control.Revision, protocol.WithdrawMessagePayload{}, app.WithdrawMessage{})
	_, seq := e.message("system", "next processing trigger", "")
	packet := e.prepare(seq)
	if strings.Contains(packet.Text, "WITHDRAWN_INSTRUCTION_MARKER") {
		t.Fatalf("withdrawn input was reinjected:\n%s", packet.Text)
	}
}
