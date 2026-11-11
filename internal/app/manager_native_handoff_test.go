package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestNativeBusinessHandoffRequiresBothAcknowledgements(t *testing.T) {
	for _, ackFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "consumer_first_lost_http_ack", true: "runner_first_restart_before_consumer"}[ackFirst], func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "start", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			claim := func(p protocol.ManagerNativeClaimPayload) protocol.Operation {
				return h.runAs(as, protocol.OpManagerNativeClaim, p, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
			}
			for range 2 {
				op := claim(protocol.ManagerNativeClaimPayload{})
				var request protocol.ManagerNativeRequest
				if json.Unmarshal(op.Result, &request) != nil || request.ID != id {
					t.Fatal("claim response loss changed the command", op)
				}
			}
			reply := protocol.ManagerNativeReply{ID: id, Method: "start", Result: json.RawMessage(`{"ID":"actual-native-thread"}`)}
			send := func() protocol.Operation {
				return h.runAs(as, protocol.OpManagerNativeReply, reply, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
			}
			if op := send(); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			acknowledge := func() {
				op := claim(protocol.ManagerNativeClaimPayload{AcknowledgedIDs: []string{id}, AcknowledgeOnly: true})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if ackFirst {
				acknowledge()
				h.app = New(h.store, h.ids, h.clock.Now)
			}
			result, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, id)
			if err != nil || !ready || !sameBusinessDefinition(result, reply) {
				t.Fatal("actual business handoff lost", result, ready, err)
			}
			if !ackFirst {
				if h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND coordinator_consumed=1 AND runner_acked=0 AND payload_json='{}' AND result_json IS NULL`, id) != 1 {
					t.Fatal("unacknowledged command did not compact")
				}
				if op := send(); op.Status != protocol.OpSucceeded {
					t.Fatal("lost original HTTP ACK could not be acknowledged", op)
				}
				acknowledge()
			}
			acknowledge() // acknowledgement loss is itself repeatable, with no dispatch.
			if h.count("manager_native_requests") != 0 {
				t.Fatal("settled RPC history retained")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				raw, err := store.NativeThread(t.Context(), tx.Q(), as.RunID)
				if raw != string(reply.Result) {
					t.Error("actual session fact lost", raw)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeUnknownCommandSurvivesTransportAcknowledgement(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "start", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	h.runAs(as, protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	reply := protocol.ManagerNativeReply{ID: id, Method: "start", Error: "provider outcome is not known"}
	if op := h.runAs(as, protocol.OpManagerNativeReply, reply, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	result, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, id)
	if err != nil || !ready || result.Error != reply.Error {
		t.Fatal(result, ready, err)
	}
	h.runAs(as, protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{AcknowledgedIDs: []string{id}, AcknowledgeOnly: true}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND status='unknown' AND coordinator_consumed=0 AND runner_acked=0 AND result_json IS NOT NULL`, id) != 1 {
		t.Fatal("transport acknowledgement discarded unknown external work")
	}
}
